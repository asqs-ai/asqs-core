package pipeline

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/llm/tokens"
)

type payloadAuditor struct {
	steps    []string
	payloads []map[string]interface{}
}

func (p *payloadAuditor) Log(_ context.Context, step string, payload interface{}) {
	p.steps = append(p.steps, step)
	m, _ := payload.(map[string]interface{})
	p.payloads = append(p.payloads, m)
}

func (p *payloadAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	p.Log(ctx, step, payload)
}

// A known model must yield a bounded budget below its window (output reservation and safety margin
// subtracted), and the counter must be installed so section spends are measured consistently.
func TestResolvePromptBudget_knownModelIsBounded(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.Model = "claude-sonnet-5"
	fo := resolvePromptBudget(cfg, retrieval.FormatOptions{})
	if fo.MaxContextTokens <= 0 || fo.MaxContextTokens >= 200000 {
		t.Fatalf("MaxContextTokens = %d, want bounded below the 200k window", fo.MaxContextTokens)
	}
	if fo.TokenCounter == nil {
		t.Fatal("TokenCounter not installed; every section would fall back to the default heuristic independently")
	}
}

// An unknown model with no configured cap must stay unbounded — a guessed limit could silently
// truncate a valid prompt, which is worse than the pre-budget behaviour.
func TestResolvePromptBudget_unknownModelStaysUnbounded(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "ollama"
	cfg.LLM.Model = "some-local-model"
	fo := resolvePromptBudget(cfg, retrieval.FormatOptions{})
	if fo.MaxContextTokens != 0 {
		t.Fatalf("MaxContextTokens = %d, want 0 (unbounded) for an unknown model", fo.MaxContextTokens)
	}
}

// retrieval.max_context_tokens must reach the renderer: a configured cap tighter than the model
// window wins, or the key is inert.
func TestResolvePromptBudget_configuredCapWins(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.Model = "claude-sonnet-5"
	cfg.Retrieval.MaxContextTokens = 1000
	fo := resolvePromptBudget(cfg, retrieval.FormatOptions{})
	if fo.MaxContextTokens != 1000 {
		t.Fatalf("MaxContextTokens = %d, want the configured 1000 to win over the model window", fo.MaxContextTokens)
	}
}

// The audit record is the observability deliverable: prompt size always, budget attribution when
// bounded, and no budget keys when unbounded (so a reader can tell the two apart).
func TestAuditPromptBudget_payloadShape(t *testing.T) {
	ctx := context.Background()
	prompt := "target method body and its dependencies"

	a := &payloadAuditor{}
	b := tokens.NewBudget(100, tokens.For("", ""))
	b.Spend(tokens.SectionTarget, prompt)
	auditPromptBudget(ctx, a, "a.B#c", prompt, b)
	if len(a.steps) != 1 || a.steps[0] != "generate.prompt_budget" {
		t.Fatalf("steps = %v, want exactly one generate.prompt_budget", a.steps)
	}
	p := a.payloads[0]
	for _, k := range []string{"fq_name", "prompt_tokens", "prompt_bytes", "counter", "budget_tokens", "sections", "over_budget"} {
		if _, ok := p[k]; !ok {
			t.Errorf("bounded payload missing %q", k)
		}
	}
	if p["fq_name"] != "a.B#c" {
		t.Errorf("fq_name = %v", p["fq_name"])
	}

	a2 := &payloadAuditor{}
	auditPromptBudget(ctx, a2, "a.B#c", prompt, nil)
	p2 := a2.payloads[0]
	for _, k := range []string{"budget_tokens", "sections", "over_budget"} {
		if _, ok := p2[k]; ok {
			t.Errorf("unbounded payload must not carry %q", k)
		}
	}
	if _, ok := p2["prompt_tokens"]; !ok {
		t.Error("unbounded payload still needs prompt_tokens")
	}
}
