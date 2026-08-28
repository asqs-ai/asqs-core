package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(&config.Config{
		LLM: config.LLMConfig{Provider: "openai", APIKey: "test-key", Model: "gpt-4o-mini", BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestComplete_lengthFinishReasonIsTruncationError is the regression test for C-5: a max_tokens stop
// arrives as HTTP 200 with a parseable body, and was previously consumed as a complete artifact.
func TestComplete_lengthFinishReasonIsTruncationError(t *testing.T) {
	t.Parallel()
	// A JSON object cut mid-string — exactly what a truncated structured generation looks like.
	const partial = `{"src/test/java/OrderServiceTest.java":"package com.acme;\n\nclass OrderServiceTe`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` +
			mustJSONString(partial) + `},"finish_reason":"length"}],` +
			`"usage":{"prompt_tokens":1200,"completion_tokens":4096,"total_tokens":5296}}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{MaxTokens: 4096})
	if err == nil {
		t.Fatalf("expected a truncation error, got result %+v", res)
	}
	if res != nil {
		t.Error("a truncated completion must not also return a result")
	}
	trunc, ok := model.IsTruncatedCompletion(err)
	if !ok {
		t.Fatalf("error = %v, want *model.TruncatedCompletionError", err)
	}
	if trunc.Provider != "openai" {
		t.Errorf("Provider = %q", trunc.Provider)
	}
	if trunc.Reason != "length" {
		t.Errorf("Reason = %q", trunc.Reason)
	}
	if trunc.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want the requested cap 4096", trunc.MaxTokens)
	}
	if trunc.GotTokens != 4096 {
		t.Errorf("GotTokens = %d, want the reported completion_tokens", trunc.GotTokens)
	}
	if trunc.Content != partial {
		t.Error("partial content should be retained for audit")
	}
}

func TestComplete_normalStopReturnsResultWithStopReason(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{MaxTokens: 4096})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Content != "done" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.StopReason != "stop" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "stop")
	}
	if res.Usage == nil || res.Usage.TotalTokens != 12 {
		t.Errorf("Usage = %+v", res.Usage)
	}
}

// Other non-length finish reasons are reported but are not truncation: content_filter means the
// provider withheld output, which is a different failure needing a different response.
func TestComplete_contentFilterIsNotTruncation(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`))
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.StopReason != "content_filter" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

// mustJSONString renders s as a JSON string literal so it can be embedded in a fixture response.
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
