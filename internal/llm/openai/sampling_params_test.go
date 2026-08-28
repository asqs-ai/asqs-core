package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// The verbatim 400 body OpenAI returns when a reasoning model is sent a sampling parameter. Run
// api-8b94f2c1c04e790bec95611c7100d571 received this on every one of its 14 gaps and generated
// nothing at all.
const reasoningModelRejection = `this model has beta-limitations, temperature, top_p and n are fixed at 1, while presence_penalty and frequency_penalty are fixed at 0`

func TestModelFixesSamplingParams(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		// The models the local configs actually run.
		{"gpt-5.5", true},
		{"gpt-5.4", true},
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"o1", true},
		{"o1-preview", true},
		{"o1-mini", true},
		{"o3", true},
		{"o3-mini-2025-01-31", true},
		{"o4-mini", true},
		{"GPT-5.5", true},
		{"  gpt-5.5  ", true},
		// Azure deployments referenced by a qualified name.
		{"azure/gpt-5.5", true},
		{"deployment:o3-mini", true},

		// Chat models, which accept temperature and must keep receiving it.
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4.1", false},
		{"gpt-4-turbo", false},
		{"gpt-3.5-turbo", false},
		{"", false},
		// Prefix matching must not swallow an unrelated name that merely starts with the letters.
		{"o1x-custom", false},
		{"gpt-50-turbo", false},
		{"omni-model", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelFixesSamplingParams(tc.name); got != tc.want {
				t.Errorf("modelFixesSamplingParams(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// newTestClientForModel points a Client at a stub server and pins the chat model. The shared
// newTestClient in truncation_test.go hardcodes gpt-4o-mini, and the model is the variable here.
func newTestClientForModel(t *testing.T, chatModel string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.BaseURL = srv.URL
	cfg.LLM.Model = chatModel

	c, err := NewClientWithKeyAndModel(cfg, "test-key", chatModel)
	if err != nil {
		t.Fatalf("NewClientWithKeyAndModel: %v", err)
	}
	return c
}

// captureRequest serves one minimal completion and records the decoded request body.
func captureRequest(t *testing.T, body *map[string]any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		*body = got
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
}

// The regression: a reasoning model must not receive temperature, whatever the policy asked for.
func TestComplete_reasoningModelOmitsTemperature(t *testing.T) {
	var body map[string]any
	c := newTestClientForModel(t, "gpt-5.5", captureRequest(t, &body))

	temp := float32(0.2) // BaselineGapPolicy.DefaultTemperature, the value that killed the run
	_, err := c.Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}},
		model.CompleteOptions{Temperature: &temp},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := body["temperature"]; present {
		t.Errorf("temperature was sent to a reasoning model: %v — the API answers %q", body["temperature"], reasoningModelRejection)
	}
}

// The counterpart: a chat model must still receive it, or the temperature knob silently stops
// working for every non-reasoning deployment.
func TestComplete_chatModelKeepsTemperature(t *testing.T) {
	var body map[string]any
	c := newTestClientForModel(t, "gpt-4o", captureRequest(t, &body))

	temp := float32(0.2)
	_, err := c.Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}},
		model.CompleteOptions{Temperature: &temp},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, present := body["temperature"]
	if !present {
		t.Fatal("temperature was dropped for a chat model")
	}
	if f, ok := got.(float64); !ok || f < 0.19 || f > 0.21 {
		t.Errorf("temperature = %v, want ~0.2", got)
	}
}

// A caller that asks for nothing still sends nothing, on either family.
func TestComplete_noTemperatureRequestedSendsNone(t *testing.T) {
	for _, m := range []string{"gpt-5.5", "gpt-4o"} {
		var body map[string]any
		c := newTestClientForModel(t, m, captureRequest(t, &body))
		if _, err := c.Complete(context.Background(),
			[]model.Message{{Role: "user", Content: "hi"}},
			model.CompleteOptions{},
		); err != nil {
			t.Fatalf("%s: Complete: %v", m, err)
		}
		if _, present := body["temperature"]; present {
			t.Errorf("%s: temperature sent when none was requested: %v", m, body["temperature"])
		}
	}
}

// Capabilities must tell the truth per model, so a caller can see the knob is inert rather than
// discovering it from a 400.
func TestCapabilities_temperatureReflectsTheModel(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	if c := newTestClientForModel(t, "gpt-5.5", noop); c.Capabilities().Temperature {
		t.Error("gpt-5.5 must report Temperature: false")
	}
	if c := newTestClientForModel(t, "gpt-4o", noop); !c.Capabilities().Temperature {
		t.Error("gpt-4o must report Temperature: true")
	}
}

// MaxCompletionTokens is the sibling fix already in place; pin it so a future edit to the sampling
// logic cannot regress the field that reasoning models DO require.
func TestComplete_reasoningModelStillSendsMaxCompletionTokens(t *testing.T) {
	var body map[string]any
	c := newTestClientForModel(t, "gpt-5.5", captureRequest(t, &body))

	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: "user", Content: "hi"}},
		model.CompleteOptions{MaxTokens: 256},
	); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := body["max_completion_tokens"]; !present {
		t.Errorf("max_completion_tokens missing; body keys: %v", keysOf(body))
	}
	if _, present := body["max_tokens"]; present {
		t.Error("deprecated max_tokens was sent")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
