package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// D2. Ollama drops the OLDEST messages when a prompt exceeds num_ctx and reports nothing: the
// response carries done_reason "stop" and looks normal. For this system the oldest message is the
// system prompt — the output contract, the artifact path, the API surface — so an overflow removes
// the instructions the reply is then judged against.
func TestPromptOverflowWarning(t *testing.T) {
	const numCtx = 32768
	opts := map[string]any{"num_ctx": numCtx}

	t.Run("warns at the window", func(t *testing.T) {
		w := promptOverflowWarning(numCtx, opts)
		if w == "" {
			t.Fatal("a prompt at num_ctx must warn")
		}
		for _, want := range []string{"32768", "num_ctx", "oldest"} {
			if !strings.Contains(w, want) {
				t.Errorf("warning %q should mention %q", w, want)
			}
		}
	})

	t.Run("warns just under the window", func(t *testing.T) {
		// Whole messages are dropped, so a truncated prompt lands just below the limit rather than
		// exactly on it; an exact test would miss every real case.
		if promptOverflowWarning(numCtx-100, opts) == "" {
			t.Error("a prompt just under num_ctx must still warn")
		}
	})

	t.Run("silent for a prompt that comfortably fits", func(t *testing.T) {
		if w := promptOverflowWarning(numCtx/2, opts); w != "" {
			t.Errorf("got %q, want silence", w)
		}
	})

	t.Run("silent when num_ctx is unknown", func(t *testing.T) {
		// With the server default in force there is no number to compare against; guessing would
		// produce warnings an operator cannot act on.
		if w := promptOverflowWarning(999999, nil); w != "" {
			t.Errorf("got %q, want silence", w)
		}
		if w := promptOverflowWarning(999999, map[string]any{"num_predict": 8192}); w != "" {
			t.Errorf("got %q, want silence", w)
		}
	})

	t.Run("silent without a token count", func(t *testing.T) {
		if w := promptOverflowWarning(0, opts); w != "" {
			t.Errorf("got %q, want silence", w)
		}
	})

	t.Run("num_ctx stored as any numeric type", func(t *testing.T) {
		for _, v := range []any{numCtx, int64(numCtx), float64(numCtx), int32(numCtx)} {
			if promptOverflowWarning(numCtx, map[string]any{"num_ctx": v}) == "" {
				t.Errorf("num_ctx stored as %T was not read", v)
			}
		}
		if promptOverflowWarning(numCtx, map[string]any{"num_ctx": "32768"}) != "" {
			t.Error("a non-numeric num_ctx must not be guessed at")
		}
	})
}

// The warning must reach the CALLER, not just the log: the generator audits res.Warnings, and a
// warning that only reaches stderr repeats the mistake this whole change exists to fix.
func TestComplete_attachesPromptOverflowWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// prompt_eval_count at the configured num_ctx: the prompt filled the window.
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop","prompt_eval_count":8192,"eval_count":10}`))
	}))
	defer srv.Close()

	c, err := NewClientWithKeyAndModel(&config.Config{
		LLM: config.LLMConfig{Provider: "ollama", Model: "m", BaseURL: srv.URL, OllamaNumCtx: 8192},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want one prompt-overflow warning", res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "num_ctx") {
		t.Errorf("warning = %q", res.Warnings[0])
	}
	// The call still succeeds: this is a warning about how to read the result, not a failure.
	if res.Content != "hi" {
		t.Errorf("Content = %q", res.Content)
	}
}

// A prompt that fits must not carry a warning, or the signal becomes noise.
func TestComplete_noWarningWhenPromptFits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop","prompt_eval_count":1000,"eval_count":10}`))
	}))
	defer srv.Close()

	c, err := NewClientWithKeyAndModel(&config.Config{
		LLM: config.LLMConfig{Provider: "ollama", Model: "m", BaseURL: srv.URL, OllamaNumCtx: 8192},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "hi"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", res.Warnings)
	}
}
