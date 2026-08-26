package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func newCapturingClient(t *testing.T, respBody string, numCtx int) (*Client, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	c := newTestChatClientWithCtx(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}, numCtx)
	return c, &captured
}

// TestComplete_sendsNumPredictAndTemperature is the regression test for M-16: MaxTokens was never
// forwarded, so the fixer asking for 8192 output tokens got the server's own num_predict default —
// truncation at an unknown, unrequested limit. Temperature was likewise dropped.
func TestComplete_sendsNumPredictAndTemperature(t *testing.T) {
	c, captured := newCapturingClient(t,
		`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`, 0)

	temp := float32(0.2)
	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}},
		model.CompleteOptions{MaxTokens: 8192, Temperature: &temp}); err != nil {
		t.Fatal(err)
	}

	opts, ok := (*captured)["options"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no options object: %+v", *captured)
	}
	if got := opts["num_predict"]; got != float64(8192) {
		t.Errorf("options.num_predict = %v, want 8192", got)
	}
	if got := opts["temperature"]; got == nil {
		t.Error("options.temperature missing")
	}
}

// The client's own num_ctx must survive alongside per-request options, and must not be mutated
// across calls.
func TestComplete_mergesNumCtxWithPerRequestOptions(t *testing.T) {
	c, captured := newCapturingClient(t,
		`{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`, 32768)

	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}},
		model.CompleteOptions{MaxTokens: 4096}); err != nil {
		t.Fatal(err)
	}
	opts := (*captured)["options"].(map[string]any)
	if got := opts["num_ctx"]; got != float64(32768) {
		t.Errorf("options.num_ctx = %v, want 32768 (client default must survive)", got)
	}
	if got := opts["num_predict"]; got != float64(4096) {
		t.Errorf("options.num_predict = %v, want 4096", got)
	}

	// A second call without MaxTokens must not inherit the first call's num_predict — that would
	// mean the client's shared options map was mutated.
	if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "y"}},
		model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}
	opts2 := (*captured)["options"].(map[string]any)
	if _, present := opts2["num_predict"]; present {
		t.Error("num_predict leaked into a later request; the client options map was mutated")
	}
	if got := opts2["num_ctx"]; got != float64(32768) {
		t.Errorf("num_ctx lost on the second call: %v", got)
	}
}

// TestComplete_reportsUsage restores the cost half of the measurement loop: llm_total_tokens and
// tokens_to_stable were always 0 on Ollama, and config-codestral.local.yaml ships in the repo, so
// this was blind on a supported configuration.
func TestComplete_reportsUsage(t *testing.T) {
	c := newTestChatClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,`+
			`"done_reason":"stop","prompt_eval_count":1200,"eval_count":340}`)
	})

	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage == nil {
		t.Fatal("Usage is nil; first_wave_metrics.llm_total_tokens stays 0 on the local path")
	}
	if res.Usage.PromptTokens != 1200 || res.Usage.CompletionTokens != 340 || res.Usage.TotalTokens != 1540 {
		t.Errorf("Usage = %+v", res.Usage)
	}
}

// An older Ollama that reports no counts must not fabricate a zero Usage — a present-but-zero Usage
// is indistinguishable from a real measurement of zero.
func TestComplete_noUsageWhenServerReportsNone(t *testing.T) {
	c := newTestChatClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
	})
	res, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage != nil {
		t.Errorf("Usage = %+v, want nil when the server reported no counts", res.Usage)
	}
}

func TestCapabilities(t *testing.T) {
	c, err := NewClientWithKeyAndModel(&config.Config{
		LLM: config.LLMConfig{Provider: "ollama", Model: "qwen2.5-coder:32b"},
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	caps, declared := model.DeclaredCapabilitiesOf(c)
	if !declared {
		t.Fatal("the Ollama client must declare capabilities")
	}
	if !caps.StructuredOutput {
		t.Error("StructuredOutput must be true: Complete sends CompleteOptions.Structured as the native /api/chat `format` field")
	}
	if !caps.MaxTokens || !caps.Temperature || !caps.UsageReporting {
		t.Errorf("caps = %+v; num_predict, temperature and usage mapping are all implemented", caps)
	}
}
