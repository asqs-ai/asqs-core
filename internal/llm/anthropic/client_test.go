package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"encoding/json"
	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"io"
	"strings"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(&config.Config{
		LLM: config.LLMConfig{Provider: "anthropic", APIKey: "test-key", Model: "claude-sonnet-4-20250514", BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestNewClient_hasTimeout guards H-10: a bare &http.Client{} has no timeout, so a stalled
// connection pins its goroutine until the outer run timeout — with gap_concurrency up to 16 sharing
// an LLMLimiter of 8, a few stalls wedge the whole run.
func TestNewClient_hasTimeout(t *testing.T) {
	t.Parallel()
	c, err := NewClient(&config.Config{
		LLM: config.LLMConfig{Provider: "anthropic", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.httpClient.Timeout <= 0 {
		t.Fatal("anthropic http client must carry a timeout")
	}
}

func TestComplete_maxTokensStopReasonIsTruncationError(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"package com.acme;\n\nclass Order"}],` +
			`"stop_reason":"max_tokens","usage":{"input_tokens":900,"output_tokens":8192}}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{MaxTokens: 8192})
	if err == nil {
		t.Fatalf("expected a truncation error, got %+v", res)
	}
	trunc, ok := model.IsTruncatedCompletion(err)
	if !ok {
		t.Fatalf("error = %v, want *model.TruncatedCompletionError", err)
	}
	if trunc.Provider != "anthropic" {
		t.Errorf("Provider = %q", trunc.Provider)
	}
	if trunc.Reason != "max_tokens" {
		t.Errorf("Reason = %q", trunc.Reason)
	}
	if trunc.MaxTokens != 8192 || trunc.GotTokens != 8192 {
		t.Errorf("MaxTokens=%d GotTokens=%d", trunc.MaxTokens, trunc.GotTokens)
	}
}

func TestComplete_endTurnReturnsResult(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":3,"output_tokens":2}}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Content != "hello" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 5 {
		t.Errorf("Usage = %+v", res.Usage)
	}
}

// TestComplete_retriesTransientStatus guards the other half of H-10: Anthropic had no retry layer at
// all, so a single transient 429 lost the gap where the same failure on OpenAI was retried.
func TestComplete_retriesTransientStatus(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("Content = %q", res.Content)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server calls = %d, want 3 (two retries)", got)
	}
}

// A 400 is a request error: it must surface immediately with the provider's body, not be retried.
func TestComplete_doesNotRetryBadRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"max_tokens is too large"}}`))
	})

	_, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server calls = %d, want 1", got)
	}
}

// The request body must survive retries; without a rebuild the second attempt sends an empty body.
func TestComplete_rebuildsBodyOnRetry(t *testing.T) {
	t.Parallel()
	var lens []int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		lens = append(lens, n)
		if len(lens) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})

	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "hello"}}, model.CompleteOptions{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(lens) != 2 {
		t.Fatalf("got %d requests, want 2", len(lens))
	}
	if lens[0] == 0 || lens[0] != lens[1] {
		t.Errorf("body lengths differ across attempts: %v", lens)
	}
}

// Temperature was discarded with a literal `_ = opts.Temperature`.
func TestComplete_sendsTemperature(t *testing.T) {
	t.Parallel()
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	temp := float32(0.35)
	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}},
		model.CompleteOptions{Temperature: &temp}); err != nil {
		t.Fatal(err)
	}
	if body["temperature"] == nil {
		t.Fatal("temperature was not sent")
	}
	// Anthropic accepts [0,1]; out-of-range values are clamped rather than rejected by the API.
	hi := float32(4)
	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}},
		model.CompleteOptions{Temperature: &hi}); err != nil {
		t.Fatal(err)
	}
	if got := body["temperature"].(float64); got != 1 {
		t.Errorf("temperature = %v, want it clamped to 1", got)
	}
}

// TestComplete_neverSendsCacheControl is the seam pin: prompt caching is excluded from the open
// core, so no request from this client may ever carry a cache_control key. buildSystemBlocks
// deliberately has no cache parameter (upstream's does); this test is what catches the mechanism
// creeping back.
func TestComplete_neverSendsCacheControl(t *testing.T) {
	t.Parallel()
	var raw []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: "system", Content: "you are a test generator"},
		{Role: "user", Content: "x"},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cache_control") {
		t.Fatalf("request carries cache_control — the excluded prompt-caching mechanism crept back:\n%s", raw)
	}
	// And the system prompt must still arrive, as blocks.
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system is not a one-element block array: %v", body["system"])
	}
	blk := sys[0].(map[string]any)
	if blk["type"] != "text" || blk["text"] != "you are a test generator" {
		t.Fatalf("system block wrong: %v", blk)
	}
}

// (Upstream additionally tests the cache_control system-block marking and cache-token usage
// counting here; both belong to the prompt-caching mechanism the seam excludes and stay out.)
