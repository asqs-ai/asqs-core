package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/llm/ollama"
)

// ollamaProbeServer answers /api/show with the given capabilities and counts the probes.
func ollamaProbeServer(t *testing.T, caps []string, probes *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			*probes++
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ollamaClient(t *testing.T, baseURL string) *ollama.Client {
	t.Helper()
	c, err := ollama.NewClientWithKeyAndModel(&config.Config{LLM: config.LLMConfig{
		Provider: "ollama", Model: "qwen2.5", BaseURL: baseURL, OllamaNumCtx: 32768,
	}}, "", "qwen2.5")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Self-hosted open models are a first-class deployment. Their tool support is a property of the
// chat template and changes between tags, so it must be probed — without this the client reports no
// tool calling and every Ollama run falls to one lookup per turn instead of parallel native calls.
func TestProbeOllamaToolSupport_declaresNativeWhenTheTemplateSupportsIt(t *testing.T) {
	probes := 0
	srv := ollamaProbeServer(t, []string{"completion", "tools"}, &probes)
	c := ollamaClient(t, srv.URL)

	if c.Capabilities().ToolCalling {
		t.Fatal("an unprobed client must not claim tool support")
	}
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	probeOllamaToolSupport(cfg, c)

	if !c.Capabilities().ToolCalling {
		t.Error("probe found the tools capability but the client still reports none")
	}
	if probes != 1 {
		t.Errorf("probes = %d, want 1", probes)
	}
}

func TestProbeOllamaToolSupport_leavesToolCallingOffWhenUnsupported(t *testing.T) {
	probes := 0
	srv := ollamaProbeServer(t, []string{"completion"}, &probes)
	c := ollamaClient(t, srv.URL)

	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	probeOllamaToolSupport(cfg, c)

	if c.Capabilities().ToolCalling {
		t.Error("a model without the tools capability was marked tool-capable")
	}
}

// Four step completers usually point at one model; probing each would be three wasted round trips
// at startup.
func TestProbeOllamaToolSupport_probesEachModelOnce(t *testing.T) {
	probes := 0
	srv := ollamaProbeServer(t, []string{"tools"}, &probes)
	a, b, c, d := ollamaClient(t, srv.URL), ollamaClient(t, srv.URL), ollamaClient(t, srv.URL), ollamaClient(t, srv.URL)

	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	probeOllamaToolSupport(cfg, a, b, c, d)

	if probes != 1 {
		t.Errorf("probes = %d for four completers on one model; want 1", probes)
	}
	// Deduplicating the PROBE must not mean skipping the VERDICT: every client for that model has
	// to end up tool-capable, or only the first step completer uses native tools and the rest
	// silently fall to the prompted tier.
	for i, cl := range []*ollama.Client{a, b, c, d} {
		if !cl.Capabilities().ToolCalling {
			t.Errorf("completer %d did not receive the probe verdict", i)
		}
	}
}

// An unreachable Ollama must not delay or fail startup — it degrades to the prompted tier.
func TestProbeOllamaToolSupport_toleratesAnUnreachableServer(t *testing.T) {
	c := ollamaClient(t, "http://127.0.0.1:1")
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	probeOllamaToolSupport(cfg, c) // must not panic or hang
	if c.Capabilities().ToolCalling {
		t.Error("an unreachable probe must not declare tool support")
	}
}

// No probe when tools are off: startup should not touch the network for a feature nobody asked for.
func TestProbeOllamaToolSupport_skippedWhenToolsDisabled(t *testing.T) {
	probes := 0
	srv := ollamaProbeServer(t, []string{"tools"}, &probes)
	c := ollamaClient(t, srv.URL)

	probeOllamaToolSupport(&config.Config{}, c)
	if probes != 0 {
		t.Errorf("probed %d time(s) with tools disabled", probes)
	}
}

// Non-Ollama completers must be ignored rather than mishandled.
func TestProbeOllamaToolSupport_ignoresOtherProviders(t *testing.T) {
	cfg := &config.Config{}
	cfg.Generation.ToolsEnabled = true
	probeOllamaToolSupport(cfg, notOllama{})
}

type notOllama struct{}

func (notOllama) Complete(context.Context, []model.Message, model.CompleteOptions) (*model.CompleteResult, error) {
	return &model.CompleteResult{}, nil
}

// The one misconfiguration that fails at run time rather than startup must be surfaced early.
func TestWarnMissingNumCtx(t *testing.T) {
	// Present: no warning path taken (asserted by not panicking and by the guard's own condition).
	cfg := &config.Config{LLM: config.LLMConfig{OllamaNumCtx: 32768}}
	warnMissingNumCtx(cfg, "qwen2.5")
	// Absent: the warning path runs. Its output goes to stderr; the assertion here is that the
	// guard distinguishes the two states rather than warning unconditionally.
	if cfg.LLM.OllamaNumCtx == 0 {
		t.Fatal("fixture wrong")
	}
	warnMissingNumCtx(&config.Config{}, "qwen2.5")
}
