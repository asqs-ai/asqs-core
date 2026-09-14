package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/audit"
	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func streamingClient(t *testing.T, url, timeout string) *Client {
	t.Helper()
	c, err := NewClientWithKeyAndModel(&config.Config{
		LLM: config.LLMConfig{Provider: "ollama", Model: "m", BaseURL: url, HTTPTimeout: timeout},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The reply arrives as newline-delimited frames, each carrying a delta. The counts and the stop
// reason live on the last one.
func TestComplete_assemblesStreamedFrames(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, frame := range []string{
			`{"message":{"role":"assistant","content":"Hello"}}`,
			`{"message":{"content":", "}}`,
			`{"message":{"content":"world"}}`,
			`{"message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":3}`,
		} {
			fmt.Fprintln(w, frame)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)

	out, err := streamingClient(t, srv.URL, "1m").Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "Hello, world" {
		t.Errorf("content = %q; want the concatenated deltas", out.Content)
	}
	if out.StopReason != "stop" {
		t.Errorf("stop reason = %q", out.StopReason)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 11 || out.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v; want 11/3 from the final frame", out.Usage)
	}
}

// A server that answers in one frame — which is what a non-streaming reply looks like — still works.
func TestComplete_acceptsASingleFrameReply(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(srv.Close)

	out, err := streamingClient(t, srv.URL, "1m").Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
}

// The budget bounds SILENCE, not total time: a server that keeps delivering must not be cut off for
// taking longer than the timeout to finish.
func TestComplete_aSlowButLiveStreamOutlivesTheTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 6; i++ {
			fmt.Fprintf(w, "{\"message\":{\"content\":\"%d\"}}\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(120 * time.Millisecond)
		}
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(srv.Close)

	// Total time is ~720ms, well past the 300ms budget; no single gap comes close to it.
	out, err := streamingClient(t, srv.URL, "300ms").Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("a stream that never went quiet was cut off: %v", err)
	}
	if out.Content != "012345" {
		t.Fatalf("content = %q", out.Content)
	}
}

// A server that accepts the request and then goes quiet is the case this exists for. It must fail
// after ONE budget, not after budget × attempts, and say what happened.
func TestComplete_reportsAStalledStream(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"partial"}}`)
		w.(http.Flusher).Flush()
		<-release // never another byte
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	start := time.Now()
	_, err := streamingClient(t, srv.URL, "250ms").Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err == nil {
		t.Fatal("a stalled stream must fail")
	}
	if !strings.Contains(err.Error(), "no data for") {
		t.Errorf("error = %q; want it to name the silence", err)
	}
	// Five attempts at 250ms plus backoff, not five attempts at a full completion timeout.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("took %s; the budget is meant to bound each attempt", elapsed)
	}
}

// recordingAuditor captures the rows the client writes.
type recordingAuditor struct {
	mu   sync.Mutex
	rows []string
}

func (a *recordingAuditor) Log(_ context.Context, step string, _ interface{}) {
	a.mu.Lock()
	a.rows = append(a.rows, step)
	a.mu.Unlock()
}
func (a *recordingAuditor) LogError(ctx context.Context, step string, p interface{}) {
	a.Log(ctx, step, p)
}

func (a *recordingAuditor) count(step string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, r := range a.rows {
		if r == step {
			n++
		}
	}
	return n
}

// Retries used to be silent, so a wedged server produced one error per gap and no sign that five
// attempts had gone into it.
func TestComplete_recordsEveryRetry(t *testing.T) {
	t.Parallel()
	var attempts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("busy"))
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(srv.Close)

	rec := &recordingAuditor{}
	ctx := audit.WithAuditor(context.Background(), rec)
	out, err := streamingClient(t, srv.URL, "5s").Complete(ctx,
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
	if n := rec.count("llm.ollama_retry"); n != 2 {
		t.Fatalf("recorded %d retry row(s); want one per repeated attempt (2)", n)
	}
}

// A 500 is not retriable and must surface immediately, as before.
func TestComplete_doesNotRetryANonRetriableStatus(t *testing.T) {
	t.Parallel()
	var attempts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"model runner has unexpectedly stopped"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := streamingClient(t, srv.URL, "5s").Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v; want the 500 surfaced verbatim", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("made %d attempts at a non-retriable status; want 1", attempts)
	}
}

func TestReadChatStream_rejectsAnEmptyStream(t *testing.T) {
	t.Parallel()
	if _, err := readChatStream(strings.NewReader("")); err == nil {
		t.Fatal("a connection closed without a frame is not a completion")
	}
	var frame chatResponse
	if err := json.Unmarshal([]byte(`{"done":true}`), &frame); err != nil {
		t.Fatal(err)
	}
}
