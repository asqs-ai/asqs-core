package pipeline

import (
	"context"
	"fmt"

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
	// A human-readable message, like every other audit line. Seventeen generate.prompt_budget rows
	// in the asqs-core audit.log of 2026-09-03 rendered as blank lines in any view keyed on
	// `message`, which is where an operator first looks.
	msg := fmt.Sprintf("Generation prompt for %s: %d tokens (%d bytes, counter %s)", fqName, total, len(prompt), counter.Name())
	if b != nil && !b.Unbounded() {
		payload["budget_tokens"] = b.Total()
		payload["sections"] = b.Breakdown()
		payload["over_budget"] = total > b.Total()
		msg += fmt.Sprintf(" of a %d-token budget", b.Total())
		if total > b.Total() {
			msg += " — OVER BUDGET"
		}
	}
	payload["message"] = msg + "."
	audit.Log(ctx, "generate.prompt_budget", payload)
}
