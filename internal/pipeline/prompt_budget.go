package pipeline

import (
	"context"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/generator"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/llm/tokens"
)

// resolvePromptBudget computes the input token budget for generation and installs it on fo.
//
// Returns fo unchanged when no budget can be established — an unknown model with no configured cap
// means today's unbounded behaviour rather than a guessed limit that could silently truncate a
// perfectly valid prompt.
func resolvePromptBudget(cfg *config.Config, fo retrieval.FormatOptions) retrieval.FormatOptions {
	if cfg == nil {
		return fo
	}
	fo.TokenCounter = tokens.For(cfg.LLM.Provider, cfg.LLM.Model)
	fo.MaxContextTokens = tokens.Resolve(
		cfg.LLM.Provider,
		cfg.LLM.Model,
		generator.DefaultGenerateMaxTokens, // output reservation
		cfg.Retrieval.MaxContextTokens,
	)
	return fo
}

// auditPromptBudget records how large the assembled prompt actually is and where the tokens went.
//
// This is the deliverable that ends "nobody knows how large prompts are". Before it, there was no
// accounting at all: no counting, no per-section attribution, and no way to tell an input overflow
// (which silently removes the output contract sitting last) from any other generation failure.
func auditPromptBudget(ctx context.Context, audit runAuditor, fqName, prompt string, b *tokens.Budget) {
	if audit == nil {
		return
	}
	counter := tokens.For("", "")
	if b != nil {
		counter = b.Counter()
	}
	total := counter.Count(prompt)
	payload := map[string]interface{}{
		"fq_name":       fqName,
		"prompt_tokens": total,
		"prompt_bytes":  len(prompt),
		"counter":       counter.Name(),
	}
	if b != nil && !b.Unbounded() {
		payload["budget_tokens"] = b.Total()
		payload["sections"] = b.Breakdown()
		payload["over_budget"] = total > b.Total()
	}
	audit.Log(ctx, "generate.prompt_budget", payload)
}
