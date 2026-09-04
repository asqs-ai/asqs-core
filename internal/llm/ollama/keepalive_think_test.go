package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// TestComplete_keepAliveAndThink checks the two Ollama-only latency knobs reach the wire as TOP-LEVEL
// request fields — keep_alive as a JSON number (Ollama's api.Duration rejects the string "-1") and
// think as a boolean — and that an unset config sends neither, leaving the server default in force.
func TestComplete_keepAliveAndThink(t *testing.T) {
	t.Parallel()
	f := false
	cases := []struct {
		name          string
		keepAlive     string
		think         *bool
		wantKeepAlive string // raw JSON; "" = field must be absent
		wantThink     string // raw JSON; "" = field must be absent
	}{
		{name: "pinned and no thinking", keepAlive: "-1", think: &f, wantKeepAlive: "-1", wantThink: "false"},
		{name: "duration string", keepAlive: "30m", wantKeepAlive: `"30m"`},
		{name: "unset sends neither"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				if err := json.Unmarshal(body, &got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"x"},"done":true}`))
			}))
			t.Cleanup(srv.Close)

			cfg := &config.Config{LLM: config.LLMConfig{
				Provider:        "ollama",
				Model:           "m",
				BaseURL:         srv.URL,
				OllamaKeepAlive: tc.keepAlive,
				OllamaThink:     tc.think,
			}}
			c, err := NewClientWithKeyAndModel(cfg, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "ping"}}, model.CompleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if ka, ok := got["keep_alive"]; string(ka) != tc.wantKeepAlive || ok != (tc.wantKeepAlive != "") {
				t.Errorf("keep_alive on the wire = %s (present=%v), want %q", ka, ok, tc.wantKeepAlive)
			}
			if th, ok := got["think"]; string(th) != tc.wantThink || ok != (tc.wantThink != "") {
				t.Errorf("think on the wire = %s (present=%v), want %q", th, ok, tc.wantThink)
			}
			if _, ok := got["options"]; ok {
				t.Errorf("neither field belongs inside options; got options=%s", got["options"])
			}
		})
	}
}

func TestNewClient_rejectsMalformedKeepAlive(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{LLM: config.LLMConfig{Provider: "ollama", Model: "m", OllamaKeepAlive: "forever"}}
	_, err := NewClientWithKeyAndModel(cfg, "", "")
	if err == nil || !strings.Contains(err.Error(), "ollama_keep_alive") {
		t.Fatalf("expected an ollama_keep_alive error, got %v", err)
	}
}
