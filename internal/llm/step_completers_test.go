package llm

import (
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// BuildStepCompleters must hand back completers that still declare their provider's capabilities:
// they reach the generator through the limiter (and later the usage tracker), so a wrapper that
// swallowed Capabilities() would make OpenAI read as "undeclared" and put every run on the
// degraded paths — with nothing failing to show it. Asserting through the REAL provider chain
// (not a mock) is the point of this test; the wrapper-level matrix lives in the model package.
func TestBuildStepCompleters_limiterKeepsDeclarations(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.Model = "gpt-4o"
	cfg.LLM.MaxConcurrent = 5

	base, doc, gen, fixer, lim, err := BuildStepCompleters(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if lim == nil || lim.Cap() != 5 {
		t.Fatalf("limiter cap = %v, want 5 (llm.max_concurrent must size the shared limiter)", lim.Cap())
	}
	for name, c := range map[string]model.ChatCompleter{"base": base, "doc": doc, "gen": gen, "fixer": fixer} {
		if c == nil {
			t.Fatalf("%s completer is nil", name)
		}
		if _, declared := model.DeclaredCapabilitiesOf(c); !declared {
			t.Errorf("%s completer lost its capability declaration through the limiter wrapper", name)
		}
	}
}

// An empty provider returns all-nil — generation is skipped, not crashed — and no limiter is
// allocated for a run that will never call an LLM.
func TestBuildStepCompleters_emptyProviderIsAllNil(t *testing.T) {
	base, doc, gen, fixer, lim, err := BuildStepCompleters(&config.Config{})
	if err != nil || base != nil || doc != nil || gen != nil || fixer != nil || lim != nil {
		t.Fatalf("want all nil for empty provider; got base=%v doc=%v gen=%v fixer=%v lim=%v err=%v",
			base, doc, gen, fixer, lim, err)
	}
}
