package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/llm/ollama"
)

// ollamaToolProbeTimeout bounds the startup probe.
//
// Short on purpose: this runs before any work and a slow or absent Ollama must not delay the run.
// A timeout degrades to the prompted tier, which still works.
const ollamaToolProbeTimeout = 5 * time.Second

// probeOllamaToolSupport asks each distinct Ollama chat model whether its template supports tools,
// and records the answer on the client so Capabilities() reports it.
//
// It must run BEFORE the completers are wrapped: the limiter and usage wrappers are opaque, and
// SetToolCalling lives on the concrete *ollama.Client.
//
// Results are cached per (endpoint, model) so four step completers pointing at one model cost one
// HTTP call. Failures are logged and tolerated — an unreachable probe means "no native tools", and
// the prompted tier still gives the model index access.
func probeOllamaToolSupport(cfg *config.Config, completers ...model.ChatCompleter) {
	if cfg == nil || !cfg.Generation.ToolsEnabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ollamaToolProbeTimeout)
	defer cancel()

	type key struct{ endpoint, modelID string }
	// The verdict is cached, not merely the fact of having probed. Step completers usually point at
	// one model, and each is a DISTINCT client object: skipping the probe without applying its
	// answer would leave three of four on the prompted tier while the first used native tools —
	// a split that produces different behaviour per step for no visible reason.
	verdict := map[key]bool{}

	for _, cc := range completers {
		oc, ok := cc.(*ollama.Client)
		if !ok || oc == nil {
			continue
		}
		k := key{oc.ChatEndpoint(), oc.ModelID()}
		if supported, done := verdict[k]; done {
			oc.SetToolCalling(supported)
			continue
		}
		supported, reason, err := ollama.ProbeToolSupport(ctx, oc.HTTPClient(), oc.ChatEndpoint(), oc.ModelID())
		verdict[k] = supported
		oc.SetToolCalling(supported)
		logOllamaToolProbe(k.modelID, supported, reason, err)
		warnMissingNumCtx(cfg, k.modelID)
	}
}

// warnMissingNumCtx surfaces the one configuration mistake that makes Ollama tool use fail at run
// time rather than at startup.
//
// Ollama silently drops the oldest messages past its context window — no error, no done_reason. A
// tool loop grows the message stack quickly, so without an explicit num_ctx the model loses the
// original task partway through and answers confidently about something else. The client refuses
// tool calls in that state; this says so before a run starts rather than at the first lookup.
func warnMissingNumCtx(cfg *config.Config, modelID string) {
	if cfg.LLM.OllamaNumCtx > 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"ollama: llm.ollama_num_ctx is unset and generation tools are enabled — tool calls on %s "+
			"will be refused. Set it (e.g. 32768); Ollama silently truncates past the window and a "+
			"tool loop overflows a default one quickly.\n", modelID)
}

// logOllamaToolProbe reports the verdict next to the existing endpoint log, so an operator can see
// immediately whether their model will use tools rather than discovering it from output quality.
func logOllamaToolProbe(modelID string, supported bool, reason string, err error) {
	switch {
	case supported:
		fmt.Fprintf(os.Stderr, "ollama: model %s supports native tool calling\n", modelID)
	case err != nil:
		fmt.Fprintf(os.Stderr, "ollama: could not probe tool support for %s (%v); falling back to prompted tools\n",
			modelID, err)
	default:
		r := strings.TrimSpace(reason)
		if r == "" {
			r = "no tools capability reported"
		}
		fmt.Fprintf(os.Stderr, "ollama: %s — falling back to prompted tools\n", r)
	}
}
