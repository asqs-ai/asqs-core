package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/generator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// toolLoopFromConfig resolves the generation tool-loop bounds for a provider.
//
// It is the only place cfg.Generation is read, so the three-tier resolution and the bounds stay
// together: a caller cannot accidentally enable tools while leaving the caps at zero, because the
// loop's own defaults apply to any unset bound.
//
// The returned reason is non-empty whenever the model gets less than native tool access, and the
// caller audits it (see auditToolMode).
func toolLoopFromConfig(cfg *config.Config, cc model.ChatCompleter) (tools.LoopOptions, string) {
	if cfg == nil {
		return tools.LoopOptions{Mode: tools.ModeOneShot}, "no configuration"
	}
	caps, declared := model.DeclaredCapabilitiesOf(cc)
	return tools.LoopOptionsFor(tools.GenerationBounds{
		ToolsEnabled:         cfg.Generation.ToolsEnabled,
		PromptedToolsEnabled: cfg.Generation.PromptedToolsEnabled,
		MaxToolTurns:         cfg.Generation.MaxToolTurns,
		MaxToolCallsPerTurn:  cfg.Generation.MaxToolCallsPerTurn,
		MaxToolCallsPerRun:   cfg.Generation.MaxToolCallsPerRun,
		MaxToolResultChars:   cfg.Generation.MaxToolResultChars,
	}, caps, declared)
}

// buildGenerationTools assembles the read-only tool registry for generation, or nil when the
// pipeline cannot support it.
//
// Nil rather than an empty registry: LLMGenerator treats a nil registry as the one-shot path, so a
// run missing a store behaves exactly as it did before tools existed instead of advertising tools
// that would fail on every call.
//
// (Upstream additionally wires web access and the build-classpath surface into the registry; those
// arrive with CP47 and CP49.)
func buildGenerationTools(cfg *config.Config, meta *metadata.Store, emb *embeddings.Store, embedder model.Embedder, repoID, lang, repoRoot string) tools.ToolInvoker {
	if cfg == nil || !cfg.Generation.ToolsEnabled {
		return nil
	}
	return buildToolRegistry(meta, emb, embedder, repoID, lang, repoRoot)
}

// buildToolRegistry assembles the read-only registry shared by both loops, or nil when the pipeline
// cannot support it.
func buildToolRegistry(meta *metadata.Store, emb *embeddings.Store, embedder model.Embedder, repoID, lang, repoRoot string) tools.ToolInvoker {
	// Metadata is what makes four of the five tools answerable; without it there is nothing worth
	// advertising.
	if meta == nil {
		return nil
	}
	reg := &tools.Registry{
		Meta:     meta,
		RepoID:   repoID,
		Lang:     lang,
		RepoRoot: repoRoot,
	}
	if emb != nil {
		reg.Chunks = emb
	}
	// The embedder is optional: search_code degrades to the lexical channel without it, which still
	// answers the literal-identifier searches that dominate this tool's use.
	if embedder != nil {
		reg.Embedder = embedder
	}
	return reg
}

// auditToolMode records which tool tier the run resolved to.
//
// Every tier below native is a real loss of capability, and discovering weeks later from a quality
// regression that a provider silently sat on the prompted path is far more expensive than one line
// at startup. Native resolves with an empty reason and is logged as such rather than skipped, so the
// audit trail always answers "what could the model do on this run".
func auditToolMode(ctx context.Context, audit runAuditor, mode tools.Mode, reason string) {
	auditToolModeFor(ctx, audit, "Generation", "generate.tool_mode", mode, reason)
}

func auditToolModeFor(ctx context.Context, audit runAuditor, who, step string, mode tools.Mode, reason string) {
	if audit == nil {
		return
	}
	msg := fmt.Sprintf("%s tool access: %s.", who, mode)
	payload := map[string]interface{}{"message": msg, "tool_mode": string(mode)}
	if reason != "" {
		payload["message"] = fmt.Sprintf("%s tool access: %s (%s).", who, mode, reason)
		payload["reason"] = reason
	}
	if mode == tools.ModeOneShot {
		// One-shot means no index access at all. That is a configuration outcome worth surfacing
		// distinctly from a mere tier downgrade.
		audit.Log(ctx, "llm.capability_degraded", payload)
		return
	}
	audit.Log(ctx, step, payload)
}

// appendStructuredDeferralNote extends a tool-mode resolution reason when the provider cannot
// honour Structured and Tools on one request, so the run-level tool_mode audit line names the
// trade-off in force.
//
// It exists because the failure it describes was invisible for a full upstream run: the tool mode
// said "native", every request said native, and zero tool calls happened — the schema grammar had
// silently excluded the tool-call syntax. The loop now defers Structured to the final turn on such
// providers (see tools.CompleteWithTools); this line is how an operator learns that from the audit
// instead of from a probe.
//
// structuredRequested is the RESOLVED decision for this run, not the provider's capability:
// deferral only happens to a schema that is actually sent, so when structured output resolved off
// the note is a false claim — upstream shipped it that way once and the note misdirected a
// post-mortem toward a grammar that was never in play.
func appendStructuredDeferralNote(reason string, cc model.ChatCompleter, structuredRequested bool) string {
	if !structuredRequested {
		return reason
	}
	caps, declared := model.DeclaredCapabilitiesOf(cc)
	if !declared || caps.StructuredWithTools || !caps.StructuredOutput {
		return reason
	}
	note := "structured output deferred to the final tool-free turn (provider cannot combine it with tools)"
	if strings.TrimSpace(reason) == "" {
		return note
	}
	return reason + "; " + note
}

// generatorHasTools reports whether the configured generator will actually offer tools this run.
//
// It checks the RESOLVED loop mode, not the config flag. A run where the provider fell back to
// one-shot — tools disabled, or no registry bound — must keep the inlined dependency bodies, since
// there is no way for the model to fetch what an inventory would merely name. Getting this wrong in
// that direction produces a context that promises lookups nobody can perform.
func generatorHasTools(g *generator.LLMGenerator) bool {
	if g == nil || g.Tools == nil {
		return false
	}
	return g.ToolLoop.Mode == tools.ModeNative || g.ToolLoop.Mode == tools.ModePrompted
}

// DefaultFixerMaxToolTurns is the fixer's turn cap when the config leaves it at 0. Smaller than the
// generator's tools.DefaultMaxToolTurns (4): a fix attempt starts from a narrower question — one
// error log against one artifact — and the whole exchange runs inside the evaluator's iteration
// budget, so each extra turn multiplies across fix attempts, not just gaps.
const DefaultFixerMaxToolTurns = 3

// fixerToolLoopFromConfig resolves the FIXER's tool loop bounds against a provider's declared
// capabilities. Same resolution ladder as generation (native → prompted → one-shot, reason returned
// for auditing); only the enable flag and the turn default differ.
//
// The completer is the FIXER's completer, which per-step config may route to a different provider
// than generation — so the resolved mode can legitimately differ between the two loops in one run.
func fixerToolLoopFromConfig(cfg *config.Config, cc model.ChatCompleter) (tools.LoopOptions, string) {
	if cfg == nil {
		return tools.LoopOptions{Mode: tools.ModeOneShot}, "no configuration"
	}
	turns := cfg.Generation.FixerMaxToolTurns
	if turns <= 0 {
		turns = DefaultFixerMaxToolTurns
	}
	caps, declared := model.DeclaredCapabilitiesOf(cc)
	return tools.LoopOptionsFor(tools.GenerationBounds{
		ToolsEnabled:         cfg.Generation.FixerToolsEnabled,
		PromptedToolsEnabled: cfg.Generation.PromptedToolsEnabled,
		MaxToolTurns:         turns,
		MaxToolCallsPerTurn:  cfg.Generation.MaxToolCallsPerTurn,
		MaxToolCallsPerRun:   cfg.Generation.MaxToolCallsPerRun,
		MaxToolResultChars:   cfg.Generation.MaxToolResultChars,
	}, caps, declared)
}

// buildFixerTools is the fixer's gate over the same registry. A separate gate, one shared registry
// builder: generation and fixing are toggled independently for the A/B, but a registry that
// answered differently for the two would make "the fixer looked it up" mean something different
// from "the generator looked it up".
func buildFixerTools(cfg *config.Config, meta *metadata.Store, emb *embeddings.Store, embedder model.Embedder, repoID, lang, repoRoot string) tools.ToolInvoker {
	if cfg == nil || !cfg.Generation.FixerToolsEnabled {
		return nil
	}
	return buildToolRegistry(meta, emb, embedder, repoID, lang, repoRoot)
}

// auditFixerToolMode is auditToolMode for the fixer's loop, under its own step so a reader can tell
// which of the two loops degraded.
func auditFixerToolMode(ctx context.Context, audit runAuditor, mode tools.Mode, reason string) {
	auditToolModeFor(ctx, audit, "Fixer", "fix.tool_mode", mode, reason)
}
