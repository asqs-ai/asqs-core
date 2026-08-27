package pipeline

import (
	"context"
	"fmt"
	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// fixBackoffDuration parses runner.fix_backoff. An unparseable value is treated as "no wait"
// rather than failing the run: pacing is an optimisation, and a typo in it should not cost a run.
func fixBackoffDuration(s string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// errorLogSummarizer builds the summariser the evaluator uses for oversized error logs, or nil
// when the feature is off or no fixer completer exists.
//
// The summary is attached BESIDE the raw compiler text in both the audit row and the fix prompt,
// never in place of it — summaries have misdiagnosed before, and the raw log stays authoritative.
func errorLogSummarizer(cfg *config.Config, cc model.ChatCompleter) func(context.Context, string) (string, error) {
	if cfg == nil || cfg.Runner.DisableErrorLogLLMSummary || cc == nil {
		return nil
	}
	return func(ctx context.Context, text string) (string, error) {
		res, err := cc.Complete(ctx, []model.Message{
			{Role: model.RoleSystem, Content: "You summarize software test/build failure logs for operators. Reply with plain text only, at most 80 short lines: (1) probable root cause, (2) first failing test or error code if visible, (3) file:line references if present. Omit duplicated stack frames."},
			{Role: model.RoleUser, Content: text},
		}, model.CompleteOptions{MaxTokens: 1024})
		if err != nil || res == nil {
			return "", err
		}
		return strings.TrimSpace(res.Content), nil
	}
}

// resolveExtendTarget decides whether this gap extends an existing test file, and returns that
// file's body when it does.
//
// The decision is made BEFORE generation because it changes the request: extending asks for the new
// methods only, creating asks for a whole file. It is deliberately conservative — the path must
// already be on disk AND look like a test artifact, so a gap can never "extend" production source.
func resolveExtendTarget(item *retrieval.TestPlanItem, gen *generator.LLMGenerator, repoAbs string) (path, body string, ok bool) {
	if item == nil || gen == nil || strings.TrimSpace(repoAbs) == "" {
		return "", "", false
	}
	target, _, _ := generator.ExistingOrSuggestedTestPath(item, gen.TestFramework, gen.E2EFramework, repoAbs, false)
	target = strings.TrimSpace(filepath.ToSlash(target))
	if target == "" {
		return "", "", false
	}
	b, err := os.ReadFile(filepath.Join(repoAbs, filepath.FromSlash(target)))
	if err != nil {
		// Nothing there yet: this is a create, which is the common case.
		return "", "", false
	}
	return target, string(b), true
}

// planItemSourceFile is the symbol-under-test's file, which the writer uses to refuse writing unit
// tests into production source.
func planItemSourceFile(item *retrieval.TestPlanItem) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	return filepath.ToSlash(strings.TrimSpace(item.Gap.Symbol.File))
}

// reconcileDuplicateArtifacts reports — and, when configured, repairs — test files that duplicate
// one another under two naming conventions.
//
// Report-only is the default and is worth shipping on its own: nothing surfaces these pairs today,
// and both members match the build's default test includes, so both run. Deletion is the only
// source-removing action in the system, so it is gated twice: by config, and by provenance — a file
// this tool has no record of writing is never removed, because it may be someone's real test.
//
// It runs between the index and the plan: after indexing (so CurrentFiles reflects the tree the run
// will work on) and before generation (so nothing this run writes is counted as a duplicate).
func reconcileDuplicateArtifacts(ctx context.Context, cfg *config.Config, audit runAuditor, repoAbs, lang string, files []indexer.FileVersion) {
	if cfg == nil || strings.TrimSpace(repoAbs) == "" || len(files) == 0 {
		return
	}
	generated := genmanifest.LoadSet(repoAbs)
	// The framework is only used to map a test path back to its source; core does not detect one
	// (LLMGenerator.TestFramework is unset here too), and the mapper handles an empty value.
	groups := generator.FindDuplicateTestArtifacts(files, lang, "", repoAbs, generated)
	if len(groups) == 0 {
		return
	}
	reconcilable := 0
	described := make([]map[string]interface{}, 0, len(groups))
	for _, g := range groups {
		described = append(described, generator.DescribeDuplicateGroup(g))
		if g.Reconcilable() {
			reconcilable++
		}
	}
	if audit != nil {
		audit.Log(ctx, "index.duplicate_test_artifacts", map[string]interface{}{
			"message": fmt.Sprintf(
				"Found %d duplicate test artifact group(s); %d can be reconciled (every redundant member is recorded as ASQS-authored). Both members of a pair match the build's default test includes, so both run.",
				len(groups), reconcilable),
			"groups":              described,
			"reconcile_enabled":   cfg.Runner.ReconcileDuplicateTestArtifacts,
			"groups_total":        len(groups),
			"groups_reconcilable": reconcilable,
		})
	}
	fmt.Fprintf(os.Stderr, "asqs-core: %d duplicate test artifact group(s) found, %d reconcilable\n", len(groups), reconcilable)
	if !cfg.Runner.ReconcileDuplicateTestArtifacts {
		return
	}
	for _, res := range generator.ReconcileDuplicateTestArtifacts(repoAbs, groups, nil) {
		if audit == nil {
			continue
		}
		if len(res.Merged) > 0 {
			audit.Log(ctx, "index.duplicate_test_artifacts_reconciled", map[string]interface{}{
				"message":   fmt.Sprintf("Merged %d duplicate test file(s) into %s and removed them.", len(res.Merged), res.Group.Canonical),
				"canonical": res.Group.Canonical,
				"merged":    res.Merged,
			})
		}
		if len(res.Skipped) > 0 {
			audit.Log(ctx, "index.duplicate_test_artifacts_skipped", map[string]interface{}{
				"message":   fmt.Sprintf("Left %d duplicate test file(s) in place next to %s.", len(res.Skipped), res.Group.Canonical),
				"canonical": res.Group.Canonical,
				"skipped":   res.Skipped,
			})
		}
	}
}

// resolveFixerStructuredOutput decides whether the fixer asks for schema-constrained JSON, and
// records the decision on EVERY path.
//
// The silent path was the problem: a post-mortem showed `structured_output_requested: false` with
// nothing anywhere saying why, so the reader could not tell a deliberate configuration from a
// provider-driven downgrade. Each branch below logs, including the one that changes nothing.
//
// grammarRisk is true when structured output stays ON against a provider that enforces the schema
// as a GRAMMAR rather than treating it as a hint. On such a provider the constraint biases replies
// toward whole-file reproduction and suppresses tool-call syntax, so the risk is worth naming in
// every fix request rather than discovering from output quality.
//
// **Core divergence, deliberate:** upstream additionally defaults structured output OFF on Ollama
// unless the operator set the key explicitly, which it can tell because its policy key is a
// *bool. Core's `runner.disable_structured_fix_output` is a plain bool, where absent and
// explicitly-false are the same value — so core honours the key as written and flags the risk
// loudly instead of silently overriding a setting the operator may have chosen. Revisit when
// CP38 re-keys the config.
func resolveFixerStructuredOutput(ctx context.Context, cfg *config.Config, audit runAuditor) (disableStructured, grammarRisk bool) {
	provider := llm.EffectiveProviderForStep(cfg, llm.StepFixer)
	log := func(disable, risk bool, reason, detail string) {
		if audit == nil {
			return
		}
		state := "on"
		if disable {
			state = "off"
		}
		audit.Log(ctx, "evaluator.fixer_structured_output_resolved", map[string]interface{}{
			"message":           fmt.Sprintf("Fixer structured output: %s (%s).", state, detail),
			"structured_output": !disable,
			"grammar_risk":      risk,
			"provider":          provider,
			"reason":            reason,
		})
	}
	switch {
	case cfg == nil:
		log(true, false, "no_configuration", "no configuration")
		return true, false
	case cfg.Runner.DisableStructuredFixOutput:
		log(true, false, "config_off", "runner.disable_structured_fix_output is set")
		return true, false
	case provider == "ollama":
		log(false, true, "ollama_grammar_risk", "on, but Ollama enforces the schema as a grammar: replies bias toward whole-file reproduction")
		return false, true
	default:
		p := provider
		if p == "" {
			p = "unknown"
		}
		log(false, false, "provider_composes", "on; provider "+p+" treats json_schema as a hint, not a grammar")
		return false, false
	}
}
