package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func newTestChatClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClientWithKeyAndModel(&config.Config{
		LLM: config.LLMConfig{Provider: "ollama", Model: "qwen2.5-coder:32b", BaseURL: srv.URL},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestComplete_doneReasonLengthIsTruncationError covers the Ollama half of C-5.
func TestComplete_doneReasonLengthIsTruncationError(t *testing.T) {
	t.Parallel()
	c := newTestChatClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"public class Ord"},"done":true,"done_reason":"length"}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{MaxTokens: 8192})
	if err == nil {
		t.Fatalf("expected a truncation error, got %+v", res)
	}
	trunc, ok := model.IsTruncatedCompletion(err)
	if !ok {
		t.Fatalf("error = %v, want *model.TruncatedCompletionError", err)
	}
	if trunc.Provider != "ollama" {
		t.Errorf("Provider = %q", trunc.Provider)
	}
	if trunc.Reason != "length" {
		t.Errorf("Reason = %q", trunc.Reason)
	}
	// MaxTokens is now forwarded as num_predict, so it IS the cap that was hit.
	if trunc.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want the requested 8192 (now sent as num_predict)", trunc.MaxTokens)
	}
	if trunc.Content != "public class Ord" {
		t.Errorf("partial content not retained: %q", trunc.Content)
	}
}

func TestComplete_doneReasonStopReturnsResult(t *testing.T) {
	t.Parallel()
	c := newTestChatClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.StopReason != "stop" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

// Older Ollama builds omit done_reason entirely; that must stay a normal completion.
func TestComplete_missingDoneReasonIsNotTruncation(t *testing.T) {
	t.Parallel()
	c := newTestChatClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.StopReason != "" {
		t.Errorf("StopReason = %q, want empty", res.StopReason)
	}
}

// newTestChatClientWithCtx is newTestChatClient with an explicit llm.ollama_num_ctx, so tests can
// assert the client's default options survive alongside per-request ones.
func newTestChatClientWithCtx(t *testing.T, handler http.HandlerFunc, numCtx int) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &config.Config{LLM: config.LLMConfig{Provider: "ollama", Model: "qwen2.5-coder:32b", BaseURL: srv.URL}}
	cfg.LLM.OllamaNumCtx = numCtx
	c, err := NewClientWithKeyAndModel(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
