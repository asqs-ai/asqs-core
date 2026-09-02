// Package pipeline is the linear driver for the asqs-core run: resolve repo → bootstrap →
// index → plan → generate-all → evaluate-whole-project-once (+ discard) → summary → optional ship. It replaces
// the proprietary session engine / workflow orchestration with a single straight-line flow.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"github.com/asqs/asqs-core/internal/evaluator/llmfix"
	"github.com/asqs/asqs-core/internal/generator"
	"github.com/asqs/asqs-core/internal/generator/contract"
	"github.com/asqs/asqs-core/internal/generator/extendmerge"
	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/projectintel"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/llm"
	"github.com/asqs/asqs-core/internal/llm/tokens"
	"github.com/asqs/asqs-core/internal/overview"
	"github.com/asqs/asqs-core/internal/runner"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/testbootstrap"
	"github.com/asqs/asqs-core/internal/workspace"
	csharpindexer "github.com/asqs/asqs-core/tools/csharp-indexer"
	javaindexer "github.com/asqs/asqs-core/tools/java-indexer"
	jstindexer "github.com/asqs/asqs-core/tools/js-ts-indexer"
)

// Options drives a single run.
type Options struct {
	RepoPath     string // absolute path to the (already resolved/cloned) repo working tree
	RepoID       string // e.g. owner/repo or a local id
	CommitSHA    string
	Lang         string // "" = autodetect from the file scan
	MaxGaps      int    // unit/doc gaps cap
	MaxGapsE2E   int    // e2e gaps cap (0 = skip e2e)
	GenerateDocs bool   // also generate per-symbol docs (inserted above declarations)
	Sandbox      string // "local" | "docker" (informational; sandbox built from cfg.Runner.Type)

	// AuditLogPath, when non-empty, appends every audit step WITH its structured payload as JSONL
	// to this file (see internal/audit). Empty = stderr step lines only, exactly as before.
	AuditLogPath string
	// AuditDumpPrompts restores full prompt/completion text in audit payloads (default: such
	// fields are stored as {sha256, len} — see audit.RedactPayload).
	AuditDumpPrompts bool
}

// GapOutcome is the per-gap result recorded in the summary.
type GapOutcome struct {
	Symbol    string
	Path      string
	Generated bool
	Stable    bool
	Discarded bool // removed because it could not be made to pass in the whole-project eval
	Err       string
}

// Summary is the run-level result the CLI prints and uses for the exit code / ship gate.
type Summary struct {
	Lang            string
	FilesIndexed    int
	GapsPlanned     int
	GapsGenerated   int
	GapsStable      int // generated gaps whose tests are in the (post-discard) green build
	Discarded       int // generated tests removed because they could not be made to pass
	DocsWritten     int
	OverviewWritten bool // the whole-repo overview document was generated + written (--docs)
	ProjectStable   bool // the whole project compiled + tests passed (possibly after discard)
	Iterations      int  // fix-loop iterations used by the single whole-project evaluation
	Outcomes        []GapOutcome
}

// Stable reports whether the whole project ended green (possibly after discarding failing tests)
// with at least one surviving generated artifact — the gate for shipping.
func (s Summary) Stable() bool {
	return s.ProjectStable && s.GapsGenerated > s.Discarded
}

// Run executes the pipeline against opts.RepoPath (already a local working tree).
func Run(ctx context.Context, cfg *config.Config, opts Options) (Summary, error) {
	var sum Summary
	audit, closeAudit := buildRunAuditor(opts.AuditLogPath, opts.AuditDumpPrompts)
	defer closeAudit()
	repoAbs, err := filepath.Abs(opts.RepoPath)
	if err != nil {
		return sum, fmt.Errorf("resolve repo path: %w", err)
	}

	// --- Stores -------------------------------------------------------------------------
	meta, err := cfg.OpenMetadataStore()
	if err != nil {
		return sum, fmt.Errorf("open metadata store: %w", err)
	}
	defer meta.Close()
	if err := meta.InitSchema(ctx); err != nil {
		return sum, fmt.Errorf("init metadata schema: %w", err)
	}
	emb, err := cfg.OpenEmbeddingsStore(ctx)
	if err != nil {
		return sum, fmt.Errorf("open embeddings store: %w", err)
	}
	defer emb.Close()
	// Create the pgvector chunks table, and re-dimension it when the embedding model's dimension
	// changed — e.g. switching to the nomic-embed-text fallback (768) against an older vector(1536)
	// column: alignChunksEmbeddingColumn truncates the now-incompatible vectors and ALTERs the type.
	// Without this, inserts fail with "expected 1536 dimensions, not 768" (SQLSTATE 22000).
	if err := emb.InitSchema(ctx); err != nil {
		return sum, fmt.Errorf("init embeddings schema: %w", err)
	}

	// --- LLM clients --------------------------------------------------------------------
	// All step completers share ONE concurrency limiter (llm.max_concurrent, default
	// model.DefaultLLMMaxConcurrent) so the overview goroutine plus per-symbol generation can
	// never exceed the provider's safe in-flight cap. The per-gap loop itself stays sequential —
	// the global limiter is the concurrency control (upstream deleted runner.gap_concurrency).
	_, docChat, genChat, fixerChat, _, err := llm.BuildStepCompleters(cfg)
	if err != nil {
		return sum, fmt.Errorf("llm chat client: %w", err)
	}
	// The embedder is wrapped in the content-addressed memo: a second run over unchanged content
	// issues zero embed calls. Every cache failure degrades to "embed it again".
	embedder, err := llm.NewCachedEmbedder(cfg, emb, emb.Dimension())
	if err != nil {
		return sum, fmt.Errorf("llm embedder: %w", err)
	}
	// Prune the embedding memo on startup. ~6 KB per cached vector means 1M chunks is ~6 GB, so
	// an unpruned cache is a slow-motion disk problem. Best-effort: a prune failure must never
	// prevent a run.
	pruneEmbeddingCache(emb, cfg.LLM.EmbeddingCacheRetentionDays)
	if w := llm.DimensionMismatchWarning(cfg, cfg.Database.EmbeddingsDimension); w != "" {
		fmt.Fprintf(os.Stderr, "pipeline: %s\n", w)
	}

	// --- Scan files + detect language ---------------------------------------------------
	files, err := indexer.ScanRepoForFiles(repoAbs, opts.RepoID, cfg.Indexer.SkipPathPrefixes, "", nil)
	if err != nil {
		return sum, fmt.Errorf("scan repo: %w", err)
	}
	files = indexer.FilterFileVersionsBySkipPrefixes(files, cfg.Indexer.SkipPathPrefixes)
	if len(files) == 0 {
		return sum, fmt.Errorf("no source files found in %s", repoAbs)
	}
	sum.FilesIndexed = len(files)
	nJava, nJST, nCSharp := langCounts(files)
	lang := strings.TrimSpace(opts.Lang)
	if lang == "" {
		lang = detectPrimaryLang(nJava, nJST, nCSharp)
	}
	sum.Lang = lang
	if lang == "" {
		return sum, fmt.Errorf("could not detect a supported language (java / csharp / javascript / typescript) in %s", repoAbs)
	}
	fmt.Fprintf(os.Stderr, "asqs-core: lang=%s files=%d (java=%d jst=%d csharp=%d)\n", lang, len(files), nJava, nJST, nCSharp)

	// --- Bootstrap (opt-in; OFF by default) --------------------------------------------
	// Detect + install a unit-test framework when the repo lacks one. Disabled by default because
	// it modifies build files (package.json / pom.xml / .csproj) and runs installs. Best-effort:
	// a failure is logged but never aborts the run.
	if cfg.Runner.TestFrameworkBootstrap.Enabled {
		if err := testbootstrap.Run(ctx, testbootstrap.Params{
			RepoPath:      repoAbs,
			Lang:          lang,
			Config:        &cfg.Runner.TestFrameworkBootstrap,
			RunnerTimeout: cfg.Runner.Timeout,
			Runner:        &cfg.Runner,
			RunnerType:    cfg.Runner.Type,
		}, audit); err != nil {
			// Also to the audit log, not only stderr: bootstrap can fail BEFORE it logs its first
			// event, and a reader of audit.log would then see the step simply not happen.
			audit.LogError(ctx, "test_bootstrap.run_failed", map[string]interface{}{
				"message": "Unit test framework bootstrap failed before it could set anything up; the run continues without it: " + err.Error(),
				"error":   err.Error(),
			})
			fmt.Fprintf(os.Stderr, "asqs-core: bootstrap: %v (continuing)\n", err)
		}
	}

	// --- E2E framework bootstrap (opt-in; runs when E2E gaps are requested) -------------
	// When --max-gaps-e2e > 0 and e2e_framework_bootstrap.enabled, set up the E2E stack the repo
	// lacks (C#: a dedicated e2e/ Playwright project, kept out of production projects; JS/TS:
	// Playwright/Cypress; Java: Playwright Java). RunE2EBootstrap self-gates on enabled/gaps/mode.
	// Best-effort: a failure is logged but never aborts the run.
	if opts.MaxGapsE2E > 0 {
		if err := testbootstrap.RunE2EBootstrap(ctx, testbootstrap.E2EParams{
			RepoPath:      repoAbs,
			Lang:          lang,
			Config:        &cfg.Runner.E2EFrameworkBootstrap,
			MaxGapsE2E:    opts.MaxGapsE2E,
			RunnerTimeout: cfg.Runner.Timeout,
			Runner:        &cfg.Runner,
			RunnerType:    cfg.Runner.Type,
		}, audit); err != nil {
			// Same reasoning as above, and this one is why the event exists: a path-resolution
			// failure returned here left NO audit trace at all, so a run generated Playwright
			// tests into a repository where E2E bootstrap had silently never run.
			audit.LogError(ctx, "e2e_bootstrap.run_failed", map[string]interface{}{
				"message": "E2E framework bootstrap failed before it could set anything up; generated E2E tests may import a stack that is not installed: " + err.Error(),
				"error":   err.Error(),
			})
			fmt.Fprintf(os.Stderr, "asqs-core: e2e bootstrap: %v (continuing)\n", err)
		}
	}

	// --- Index --------------------------------------------------------------------------
	langIdx, indexable, err := buildLangIndexer(ctx, cfg, repoAbs, lang, nJava, nCSharp, nJST)
	if err != nil {
		return sum, fmt.Errorf("language indexer: %w", err)
	}
	runID := fmt.Sprintf("core_%d", time.Now().UnixNano())
	// Join this run to the exact configuration that produced it (the A/B report groups on it).
	// Best-effort: a run without a recorded revision still runs, it is just invisible to ab-report.
	configRevisionID, crErr := ensureConfigRevisionForRun(ctx, meta, cfg.SourcePath)
	if crErr != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: record config revision: %v (run will not appear in ab-report)\n", crErr)
	}
	if _, err := indexer.Run(ctx, meta, emb, indexer.RunOptions{
		CurrentFiles:      files,
		RepoPath:          repoAbs,
		RepoID:            opts.RepoID,
		CommitSHA:         opts.CommitSHA,
		RunID:             runID,
		LangIndexer:       langIdx,
		Embedder:          embedder,
		EmbeddingProvider: embedProvider(cfg),
		EmbeddingModel:    embedModel(cfg),
		Audit:             audit,
		IndexablePaths:    indexable,
		ConfigRevisionID:  configRevisionID,
		// Off by default: with it disabled the chunk counts are identical to before this existed.
		// Reads only artifacts already on disk — no network, no subprocess.
		DependencyDocs: indexer.DependencyDocOptions{
			Enabled: cfg.Indexer.DependencyDocs.Enabled,
			// FROZEN (CP37): the two chunk caps are left at zero so the indexer's own
			// constants apply — 80 per dependency, 400 in total. They were pass-throughs for
			// config keys no template ever set.
			MavenRepoDir:     cfg.Indexer.DependencyDocs.MavenRepoDir,
			NuGetPackagesDir: cfg.Indexer.DependencyDocs.NuGetPackagesDir,
		},
	}); err != nil {
		return sum, fmt.Errorf("index: %w", err)
	}

	// --- Plan ---------------------------------------------------------------------------
	// Between index and plan: the tree the run will work on is known, and nothing this run writes
	// exists yet, so a duplicate found here is genuinely pre-existing.
	reconcileDuplicateArtifacts(ctx, cfg, audit, repoAbs, lang, files)

	planOpts := buildPlanOptions(cfg, lang, opts.RepoID)
	planOpts.MaxGaps = orDefault(opts.MaxGaps, 10)
	planOpts.MaxGapsE2E = opts.MaxGapsE2E
	planOpts.Audit = audit
	// retrieval.failure_hint_file was reaching PlanOptions.FailureHintFile and stopping there —
	// nothing ever opened the file, so FailureHint stayed empty and failure-localized retrieval was
	// unreachable from configuration. Reading it here is what makes the key mean anything (CP36).
	if rel := failureHintReadRelPath(cfg); rel != "" && strings.TrimSpace(planOpts.FailureHint) == "" {
		if hint := loadFailureHintFromRepoFile(repoAbs, rel); hint != "" {
			planOpts.FailureHint = hint
			audit.Log(ctx, "plan.failure_hint_loaded", map[string]interface{}{
				"message": fmt.Sprintf("Retrieval is localized by %s (%d bytes): planning weights chunks the last failure implicates.", rel, len(hint)),
				"path":    rel,
				"bytes":   len(hint),
			})
		}
	}
	// Detect the repository's unit-test naming convention once, from the indexed file list, and
	// carry it to every item. The generator's built-in default is FooTest.java; plenty of
	// repositories use FooTests.java, and on those every run wrote a sibling beside the file it
	// should have extended — after which the redirect picks on sort order ("Test.java" precedes
	// "Tests.java"), so the tool's own leftover shadows the real suite permanently. ASQS-authored
	// files are excluded from the vote, or the tool would eventually vote its own mistake into the
	// house style.
	if conv := generator.DetectTestSuffixConvention(files, lang, genmanifest.LoadSet(repoAbs)); conv.Detected() {
		planOpts.TestSuffixConvention = conv.Suffix
		fmt.Fprintf(os.Stderr, "asqs-core: %s\n", conv.Describe())
	}
	plan, err := retrieval.CreateTestPlan(ctx, meta, meta, emb, planOpts)
	if err != nil {
		return sum, fmt.Errorf("plan: %w", err)
	}
	if opts.MaxGapsE2E > 0 && retrieval.SupportsE2EGapListing(lang) {
		if e2ePlan, err := retrieval.CreateE2ETestPlan(ctx, meta, meta, emb, planOpts); err == nil && e2ePlan != nil {
			plan.Items = append(plan.Items, e2ePlan.Items...)
		}
	}
	if plan == nil || len(plan.Items) == 0 {
		fmt.Fprintln(os.Stderr, "asqs-core: no test gaps found — nothing to generate.")
		// Evaluation never ran: completed with stable/iterations untouched and metrics NULL — an
		// absent metrics row means "not measured", which must stay distinguishable from zeroes.
		completeRun(ctx, meta, runID, nil, nil, nil)
		return sum, nil
	}
	sum.GapsPlanned = len(plan.Items)

	// --- Project intel ------------------------------------------------------------------
	// Discover + rank repo docs/skills and build a markdown context block injected into
	// each gap's generation prompt. Enabled by default; errors are non-fatal.
	var piResult *projectintel.Result
	piCfg := cfg.EffectiveProjectIntel()
	if piCfg.EffectiveEnabled() {
		piIn := projectintel.Input{
			RepoAbs: repoAbs,
			// RepoID scopes doc→symbol resolution; the metadata store satisfies SymbolResolver.
			RepoID:            opts.RepoID,
			SymbolResolver:    meta,
			Lang:              lang,
			CurrentFiles:      files,
			ConfigFingerprint: piCfg.ConfigFingerprintHash(),
			LLM:               genChat,
			Opts: projectintel.Options{
				Enabled:             true,
				MaxTotalRunes:       piCfg.EffectiveMaxTotalRunes(),
				MaxDocFiles:         piCfg.EffectiveMaxDocFiles(),
				MaxSkillFiles:       piCfg.EffectiveMaxSkillFiles(),
				MinRelevanceScore:   piCfg.EffectiveMinRelevanceScore(),
				SummarizeAboveRunes: piCfg.EffectiveSummarizeAboveRunes(),
				UseEmbeddingsRank:   piCfg.UseEmbeddingsRank,
				ExtraDocGlobs:       piCfg.ExtraDocGlobs,
				ExtraSkillGlobs:     piCfg.ExtraSkillGlobs,
				CacheEnabled:        piCfg.EffectiveCacheEnabled(),
				CachePath:           piCfg.EffectiveCachePath(),
				// FROZEN (CP37, runner.policy.project_intel.force_refresh): a debug one-off that
				// bypassed the cache for a single run. Deleting the cache file does the same thing
				// without a permanent key inviting someone to leave it on.
				ForceRefresh:    false,
				FingerprintMode: piCfg.EffectiveFingerprintMode(),
			},
		}
		if piCfg.UseEmbeddingsRank {
			piIn.Embedder = embedder
		}
		if r, piErr := projectintel.Run(ctx, piIn); piErr == nil {
			piResult = r
			fmt.Fprintf(os.Stderr, "asqs-core: project-intel mode=%s docs=%d skills=%d approx_runes=%d cache_hit=%v\n",
				r.Mode, r.DocsSelected, r.SkillsSelected, r.ApproxRunes, r.CacheHit)
		} else {
			fmt.Fprintf(os.Stderr, "asqs-core: project-intel: %v (continuing without)\n", piErr)
		}
	}

	// --- Generate every gap's test, then evaluate the WHOLE project ONCE ----------------
	formatOpts := retrieval.DefaultFormatOptions()
	applyRetrievalContextCompactToFormat(&cfg.Retrieval, &formatOpts)
	formatOpts = resolvePromptBudget(cfg, formatOpts)
	// Compact once per plan, before the generation loop, so every prompt (tests and docs) sees the
	// same shrunken context and the cost is paid once per item.
	compactPlanContexts(ctx, formatOpts, audit, plan)
	// Real member signatures for the third-party types a diagnostic blames, read from the project's
	// own build inputs. Nil for a language with no provider, which is the documented no-op: the
	// prompt renders no block, exactly as before this existed. Honest degradation is the contract —
	// never a wrong or empty surface.
	// The one outbound path in this tool, resolved once and audited on every branch. The deny
	// tokens are derived from the repository's own identity so its private names cannot leave the
	// process inside a query.
	webClient := buildWebClient(ctx, cfg, audit, repoAbs, queryDenyTokens(opts.RepoID, repoAbs))

	apiSurface := apisurface.NewProviderForLang(lang)
	if apiSurface == nil {
		fmt.Fprintf(os.Stderr, "asqs-core: no API-surface provider for %s; the fixer gets no member-signature block\n", lang)
	}

	// The bootstrap contract's two halves have different authors: bootstrap states what it
	// installed, running its toolchain in an ephemeral container, while the API-surface provider
	// resolves this project's real compile classpath on the host. Only the second can tell Spring
	// Boot 3 from Spring Boot 4, so it is filled in here — once per run, before the per-gap
	// generation fan-out reads the contract, and BEFORE the audit so the audited payload is the one
	// generation will actually see. Both calls are no-ops without a contract, which is the default.
	generator.ResolveTestStackCanonicalImports(ctx, audit, apiSurface, repoAbs, lang)
	generator.AuditTestStackContract(ctx, audit, repoAbs)

	rules := contract.ByLang(lang)
	// Token usage for the first-wave metrics: generation + fixes only, matching upstream's
	// RunLLMUsage scope (the doc pass and overview deliberately stay untracked).
	runUsage := &model.UsageAccumulator{}
	trackedGen := model.NewUsageTrackingChatCompleter(genChat, runUsage)
	trackedFixer := model.NewUsageTrackingChatCompleter(fixerChat, runUsage)
	gen := &generator.LLMGenerator{
		LLM:           trackedGen,
		ContractRules: &rules,
		// runner.disable_structured_generate_output was declared, documented and never passed to
		// the generator, so setting it did nothing. (The inert-field lint matches by field name and
		// LLMGenerator has an identically named field, which is why it read as wired.) It has to be
		// right here in particular: the tool loop's structured-deferral audit below reports on the
		// schema this flag decides, and reporting a deferral for a schema that was never sent is
		// exactly the false claim that misdirected an upstream post-mortem.
		DisableStructuredGenerateOutput: cfg.Runner.DisableStructuredGenerateOutput,
		TwoPhaseTestGeneration:          cfg.Runner.TwoPhaseTestGeneration,
		RepoPath:                        repoAbs,
		Audit:                           audit,
		// Generate-side member signatures, so an invented API is contradicted before the model
		// writes it rather than by a containerised compile a round later.
		APISurface: apiSurface,
	}
	// The E2E selector inventory (generator/ui_selectors.go), assigned only when there is a store
	// to ask: meta is a concrete *metadata.Store, so assigning a nil one into the interface field
	// would leave it non-nil and holding nil, and the inventory would call through and panic.
	if meta != nil {
		gen.UISelectors = meta
		gen.RepoID = opts.RepoID
	}
	// Give the model read-only access to the index during generation.
	//
	// Retrieval otherwise assembles a context once and the model gets a single turn; measured
	// upstream against a labelled suite it delivers about half the relevant chunks, and the model
	// has no way to ask for the rest. The registry is what turns a retrieval miss into a lookup.
	//
	// The mode is audited on EVERY path, including the one where tools are off. The audit call used
	// to live inside this `reg != nil` block, so a run with tools disabled — the default — produced
	// no tool-mode event at all, and the only trace of one-shot anywhere in the log was a
	// tools_mode field buried in each evaluator.fix_request payload. That is precisely the
	// condition ResolveMode's doc calls out: "a silent downgrade is how 'tools are enabled' and
	// 'the model never called a tool' coexist for weeks without anyone noticing."
	genTools := buildGenerationTools(cfg, meta, emb, embedder, opts.RepoID, lang, repoAbs, apiSurface, webClient)
	genLoop, genReason := toolLoopFromConfig(cfg, trackedGen)
	if genTools != nil {
		gen.Tools = genTools
		gen.ToolLoop = genLoop
	}
	genMode, genReason := effectiveToolMode(genLoop, genReason, genTools != nil)
	auditToolMode(ctx, audit, genMode,
		appendStructuredDeferralNote(genReason, trackedGen, !cfg.Runner.DisableStructuredGenerateOutput))
	// With tools in play the prompt carries a high-precision core plus an INVENTORY of what can be
	// fetched, rather than every dependency body inline.
	//
	// The gate is the RESOLVED loop mode, not the config flag: a run that asked for tools but fell
	// back to one-shot — an incapable provider, no registry — must still get the inlined bodies,
	// because nothing can fetch what an inventory merely names. Getting this backwards produces a
	// context that promises lookups nobody can perform.
	formatOpts.ToolsAvailable = generatorHasTools(gen)
	// Resolved once, and recorded on every path — including the one that changes nothing, which
	// used to be silent and left a post-mortem unable to tell configuration from downgrade.
	fixerStructuredOff, fixerGrammarRisk := resolveFixerStructuredOutput(ctx, cfg, audit)
	fixer := &llmfix.Fixer{
		LLM:   trackedFixer,
		Audit: audit,
		// Two more documented keys the pipeline never passed on, so setting them did nothing —
		// the same field-name collision that hid runner.disable_structured_generate_output from
		// the inert-field lint (Fixer declares identically named fields). Neither changes default
		// behaviour; they just make the keys work. disable_structured_fix_output in particular has
		// to be right here, because the tool-mode audit below reports on the schema it decides.
		// runner.disable_multi_turn_fixer is deliberately NOT wired: its default would flip
		// MultiTurnRepair on, which is a behaviour change owned by the fixer-hardening wave.
		DisableStructuredFixOutput:  fixerStructuredOff,
		StructuredOutputGrammarRisk: fixerGrammarRisk,
		StructuredUserMessage:       cfg.Runner.FixerStructuredUserMessage,
	}
	// The fixer gets the same read-only suite, behind its own gate: generation and repair are
	// toggled independently so a fix-quality comparison can move one without the other.
	// Audited unconditionally, for the same reason as generation above — and this is the half that
	// actually differs in practice, because fixer tools are gated separately and stay off in
	// configurations that turn generation tools on.
	fixTools := buildFixerTools(cfg, meta, emb, embedder, opts.RepoID, lang, repoAbs, apiSurface, webClient)
	fixLoop, fixReason := fixerToolLoopFromConfig(cfg, trackedFixer)
	if fixTools != nil {
		fixer.Tools = fixTools
		fixer.ToolLoop = fixLoop
	}
	fixMode, fixReason := effectiveToolMode(fixLoop, fixReason, fixTools != nil)
	auditFixerToolMode(ctx, audit, fixMode,
		appendStructuredDeferralNote(fixReason, trackedFixer, !cfg.Runner.DisableStructuredFixOutput))
	sandbox := runner.NewSandboxFromConfig(cfg)
	maxFix := orDefault(cfg.Runner.StartMaxIteration, 3)

	// Formatting: format generated tests post-generate and after each LLM fix so they satisfy the
	// repo's style gates (e.g. `dotnet format --verify-no-changes`, .editorconfig treated as
	// errors, analyzers).
	formatTimeout := 2 * time.Minute
	if d, derr := time.ParseDuration(cfg.Runner.Timeout); derr == nil && d > 0 {
		formatTimeout = d
	}
	formatTarget := runner.TargetLocal
	if strings.EqualFold(strings.TrimSpace(cfg.Runner.Type), string(runner.TargetDocker)) {
		formatTarget = runner.TargetDocker
	}
	// Resolved per call, through the same resolver for every language and both targets (CP35).
	// The old EffectivePostGenerateFormatCommand only defaulted for C#, so a Java repo with
	// runner.format_command empty never wired this hook at all — the fixer's rewrite was never
	// reformatted, the next compile failed on the formatter's own validate goal, and the fix loop
	// spent its budget asking the LLM to hand-format Java. An unresolvable formatter is a no-op
	// inside FormatAfterFixForSandbox, so there is no non-empty guard any more.
	formatAfterFixHook := func(ctx context.Context, repoPath string, updatedPaths []string) error {
		resolved := runner.ResolveFormatCommand(repoPath, lang, cfg.Runner.FormatCommand, cfg.Runner.BuildTool, cfg.Runner.FormatOnlyAdded, formatTarget)
		err := runner.FormatAfterFixForSandbox(sandbox, ctx, repoPath, lang, resolved, updatedPaths, formatTimeout)
		if err != nil && errors.Is(err, runner.ErrFormatSkippedNoDotnet) {
			return fmt.Errorf("%w: %v", evaluator.ErrFormatAfterFixSkipped, err)
		}
		return err
	}
	var docGen *generator.LLMDocGenerator
	var docFmt retrieval.FormatOptions
	if opts.GenerateDocs {
		docGen = &generator.LLMDocGenerator{LLM: docChat}
		docFmt = retrieval.DefaultFormatOptions()
		docFmt.DocGeneration = true
	}

	// Overview documentation (whole-repo) runs in PARALLEL with the per-symbol test/doc generation
	// below when --docs is set. It only reads the metadata store (which the generation loop does not
	// touch) and shares the HTTP-based LLM client safely, so the two run concurrently; the generated
	// document is written after the loop. Matches asqs-go (overview generated alongside generation).
	var overviewWG sync.WaitGroup
	var overviewContent, overviewPath string
	var overviewErr error
	if opts.GenerateDocs && !cfg.Indexer.DisableOverviewDocGeneration {
		og := &overview.LLMOverviewDocGenerator{
			LLM:  genChat,
			Path: strings.TrimSpace(cfg.Indexer.OverviewDocPath),
			// FROZEN (CP37, indexer.overview_max_completion_tokens): zero keeps the
			// overview generator's own 8192 default for a full narrative.
			MaxCompletionTokensFull: 0,
			FullRewrite:             cfg.Indexer.OverviewFullRewrite,
		}
		overviewWG.Add(1)
		go func() {
			defer overviewWG.Done()
			fmt.Fprintln(os.Stderr, "asqs-core: generating overview documentation (in parallel)…")
			overviewContent, overviewPath, overviewErr = og.Generate(ctx, meta, opts.RepoID, lang, repoAbs, cfg.Indexer.OverviewMaxFilesPerSlice, cfg.Indexer.OverviewMaxIndexRunesPerSlice)
		}()
	}

	// Baseline: was the tree already red BEFORE this run generated anything?
	//
	// Without this, a repository that could not compile at minute zero spends the entire fix budget
	// on failures the run did not cause, and the audit attributes every one of them to the run.
	// Ordering is the whole point: after the plan is built and before a single artifact is written,
	// so nothing this run produces can be counted as inherited.
	baseline := evaluator.CaptureBaselineFailures(ctx, sandbox, evaluator.EvalOptions{
		RepoPath:       repoAbs,
		Lang:           lang,
		BuildTool:      cfg.Runner.BuildTool,
		CompileCommand: cfg.Runner.CompileCommand,
	})
	switch {
	case !baseline.Captured:
		// Deliberately silent: an uncaptured baseline must never read as "the tree was clean".
	case baseline.Clean:
		audit.Log(ctx, "evaluator.baseline_compile", map[string]interface{}{
			"message": "Baseline compiled before generation: every failure from here on was introduced by this run.",
			"clean":   true,
		})
	default:
		fmt.Fprintf(os.Stderr, "asqs-core: baseline did not compile before generation: %d file(s) were already failing\n", len(baseline.Paths))
		audit.Log(ctx, "evaluator.baseline_compile", map[string]interface{}{
			"message": fmt.Sprintf(
				"Baseline did NOT compile before generation: %d file(s) were already failing. Repairing them is in scope; they are not regressions introduced by this run.",
				len(baseline.Paths)),
			"clean":             false,
			"failing_paths":     baseline.Paths,
			"failure_signature": baseline.Signature,
			"summary":           baseline.Summary,
		})
	}

	// Phase 1 — generate + write every gap's test (no per-gap evaluation). Collect the unique
	// artifact paths so the whole project is compiled/tested exactly once below.
	var artifactPaths []string
	// Artifacts we appended to a file the repository already owned. Tracked separately because the
	// discard path below is os.Remove: deleting one of these would take the project's own tests
	// with it. See EvalOptions.ExtendedArtifactPaths.
	var extendedArtifactPaths []string
	seen := map[string]bool{}
	outcomeIdxByPath := map[string]int{} // normalized path -> index of the gap that first wrote it
	anyE2E := false
	docInsertsByFile := map[string][]docInsert{} // collected per-symbol docs, applied per file after the loop
	for _, item := range plan.Items {
		out := GapOutcome{Symbol: planItemSymbol(item)}
		// One budget per item. formatOpts is copied here, so items never share a budget.
		itemFmt := formatOpts
		budget := tokens.NewBudget(itemFmt.MaxContextTokens, itemFmt.CounterOrDefault())
		itemFmt.LastBudget = budget
		ctxStr := retrieval.BuildLLMContextForGap(item, itemFmt)
		if piResult != nil {
			if piMarkdown := strings.TrimSpace(projectIntelForGap(piResult, piCfg, item)); piMarkdown != "" {
				ctxStr = piMarkdown + "\n\n" + ctxStr
			}
		}
		// Extend-or-create, decided BEFORE generation because it changes what the model is asked
		// for: a whole file, or only the new methods to splice into the one already on disk.
		//
		// Without this the run writes a sibling beside the file it should have extended, and once
		// both exist the redirect picks on sort order — the tool's own leftover then shadows the
		// repository's real suite permanently.
		extendPath, existingBody, doExtend := resolveExtendTarget(item, gen, repoAbs, cfg.Runner.PreferDefaultTestSuffix)
		if doExtend {
			prefix := generator.ExtendExistingTestContextPrefix
			if item.Context != nil && len(item.Context.ExistingTestPaths) > 0 {
				prefix = fmt.Sprintf(generator.ExtendExistingRedirectPrefix, filepath.ToSlash(extendPath)) + prefix
			}
			ctxStr = prefix + existingBody + generator.ExtendExistingTestContextSuffix + ctxStr
		}
		auditPromptBudget(ctx, audit, out.Symbol, ctxStr, budget)
		content, relPath, gerr := gen.Generate(ctx, item, ctxStr)
		switch {
		case gerr != nil:
			out.Err = gerr.Error()
			auditGenerateFailed(ctx, audit, out.Symbol, "generator_error", gerr.Error())
		case strings.TrimSpace(content) == "" || strings.TrimSpace(relPath) == "":
			out.Err = "empty generation"
			auditGenerateFailed(ctx, audit, out.Symbol, "empty_generation",
				"the model returned no content, or no path to write it to")
		default:
			// Under extend semantics the target is authoritative: it is the path whose bytes were
			// read above, while the generator derives its own path from the suggester and would
			// silently write a near-duplicate sibling.
			writePath := relPath
			if doExtend {
				writePath = extendPath
			}
			wrote, written, skips, imports := extendmerge.WriteWithImportReport(repoAbs, []extendmerge.Item{{
				Path:             writePath,
				Content:          content,
				ExtendExisting:   doExtend,
				SourceSymbolFile: planItemSourceFile(item),
				// Names the payload used without importing are resolved against this project's
				// compile classpath — the same source, and the same exactly-one-candidate rule,
				// that gives generation its canonical framework imports.
				ImportResolver: func(names []string) map[string]string {
					return apisurface.ResolveSimpleNames(ctx, apiSurface, repoAbs, lang, names)
				},
				TypeExists: func(fqns []string) map[string]bool {
					return apisurface.TypesPresent(ctx, apiSurface, repoAbs, lang, fqns)
				},
			}})
			for _, sk := range skips {
				audit.Log(ctx, "generate.write_skipped", map[string]interface{}{
					"message": "Generated artifact was not written: " + sk,
					"symbol":  out.Symbol,
				})
			}
			// What the import union did when extending an existing test file. Every compile failure
			// in the run of 2026-08-29 originated here and the audit said nothing, because these
			// lines went to stderr only.
			for _, im := range imports {
				payload := map[string]interface{}{
					"message": fmt.Sprintf("Extend merge for %s: added %d import(s), refused %d.",
						im.Path, len(im.Merged), len(im.Skipped)),
					"symbol":          out.Symbol,
					"path":            im.Path,
					"merged":          im.Merged,
					"merged_count":    len(im.Merged),
					"skipped_count":   len(im.Skipped),
					"extend_existing": doExtend,
				}
				if len(im.Skipped) > 0 {
					payload["skipped"] = im.Skipped
				}
				if len(im.Inferred) > 0 {
					payload["inferred"] = im.Inferred
					payload["message"] = fmt.Sprintf("Extend merge for %s: added %d import(s) (%d inferred from use: %s), refused %d.",
						im.Path, len(im.Merged), len(im.Inferred), strings.Join(im.Inferred, ", "), len(im.Skipped))
				}
				if len(im.ShadowedNames) > 0 {
					payload["shadowed_names"] = im.ShadowedNames
					payload["message"] = fmt.Sprintf("Extend merge for %s REFUSED: %d name(s) the payload uses are already bound by a single-type import that would shadow the on-demand import it needs (JLS 7.5).",
						im.Path, len(im.ShadowedNames))
				}
				audit.Log(ctx, "generate.extend_imports", payload)
			}
			switch {
			case wrote == 0:
				out.Err = "write skipped"
				if len(skips) > 0 {
					out.Err = "write skipped: " + skips[0]
				}
			default:
				relPath = written[0]
				// Record provenance so the convention vote and the duplicate reconciler can tell
				// this run's files from the repository's own. Best-effort: an unrecorded write
				// degrades to "human-authored", which is the safe reading.
				if _, rerr := genmanifest.Record(repoAbs, runID, written); rerr != nil {
					fmt.Fprintf(os.Stderr, "asqs-core: record generated artifact provenance: %v\n", rerr)
				}
				out.Path = relPath
				out.Generated = true
				sum.GapsGenerated++
				if isE2E(item) {
					anyE2E = true
				}
				if np := normPath(relPath); !seen[np] {
					seen[np] = true
					outcomeIdxByPath[np] = len(sum.Outcomes) // index this outcome will take below
					artifactPaths = append(artifactPaths, relPath)
					if doExtend {
						extendedArtifactPaths = append(extendedArtifactPaths, relPath)
					}
				}
			}
		}
		// Per-symbol docs are in-file inserts (not sandbox-evaluated). Every failure mode here is
		// surfaced to stderr so a "0 docs" run is diagnosable instead of silently swallowed.
		if docGen != nil {
			switch {
			case item.Gap == nil || item.Gap.Symbol == nil || item.Gap.Symbol.StartLine <= 0:
				// No source anchor (e.g. an E2E/synthetic gap) — nothing to attach a doc to.
			default:
				docCtx := retrieval.BuildLLMContextForGap(item, docFmt)
				doc, _, derr := docGen.GenerateDoc(ctx, item, docCtx)
				switch {
				case derr != nil:
					fmt.Fprintf(os.Stderr, "asqs-core: docs: generate for %s: %v\n", out.Symbol, derr)
				case strings.TrimSpace(doc) == "":
					fmt.Fprintf(os.Stderr, "asqs-core: docs: empty generation for %s\n", out.Symbol)
				default:
					// Collect now; applied per file after the loop (dedup + validate + correct offsets).
					f := item.Gap.Symbol.File
					docInsertsByFile[f] = append(docInsertsByFile[f], docInsert{
						line:    item.Gap.Symbol.StartLine,
						content: doc,
						symbol:  out.Symbol,
					})
				}
			}
		}
		sum.Outcomes = append(sum.Outcomes, out)
	}

	// Apply collected per-symbol docs in one pass per file: skip symbols that already have a doc, skip
	// malformed comment blocks, and insert sorted-ascending with a running offset so multiple docs in
	// one file land at the right lines — preventing duplicate docs and split/broken /** … */ blocks.
	sum.DocsWritten = applyCollectedDocInserts(repoAbs, docInsertsByFile)

	// Overview: join the parallel generation and write the document (best-effort; never aborts the run).
	if opts.GenerateDocs {
		overviewWG.Wait()
		switch {
		case overviewErr != nil:
			fmt.Fprintf(os.Stderr, "asqs-core: overview: %v (continuing)\n", overviewErr)
		case strings.TrimSpace(overviewContent) == "":
			fmt.Fprintln(os.Stderr, "asqs-core: overview: empty content — not written.")
		default:
			rel := strings.TrimSpace(overviewPath)
			if rel == "" {
				rel = overview.DefaultOverviewPath
			}
			full := filepath.Join(repoAbs, filepath.FromSlash(rel))
			if mkErr := os.MkdirAll(filepath.Dir(full), 0o755); mkErr != nil {
				fmt.Fprintf(os.Stderr, "asqs-core: overview: mkdir %s: %v\n", rel, mkErr)
			} else if wErr := os.WriteFile(full, []byte(overviewContent), 0o644); wErr != nil {
				fmt.Fprintf(os.Stderr, "asqs-core: overview: write %s: %v\n", rel, wErr)
			} else {
				sum.OverviewWritten = true
				fmt.Fprintf(os.Stderr, "asqs-core: overview written → %s\n", rel)
			}
		}
	}

	if len(artifactPaths) == 0 {
		auditNoArtifacts(ctx, audit, sum.Outcomes)
		fmt.Fprintln(os.Stderr, "asqs-core: no test files were generated — skipping evaluation.")
		completeRun(ctx, meta, runID, nil, nil, nil)
		return sum, nil
	}

	// Post-generate format: format the freshly written test files before evaluation so a style gate
	// (dotnet format --verify-no-changes, .editorconfig-as-errors, analyzers) doesn't fail on layout.
	// Best-effort: a formatter problem is logged but never aborts the run.
	if resolved := runner.ResolveFormatCommand(repoAbs, lang, cfg.Runner.FormatCommand, cfg.Runner.BuildTool, cfg.Runner.FormatOnlyAdded, formatTarget); resolved.Command != "" {
		fmt.Fprintf(os.Stderr, "asqs-core: formatting %d generated file(s) (%s)…\n", len(artifactPaths), resolved.Command)
		if err := runner.FormatAfterFixForSandbox(sandbox, ctx, repoAbs, lang, resolved, artifactPaths, formatTimeout); err != nil && !errors.Is(err, runner.ErrFormatSkippedNoDotnet) {
			fmt.Fprintf(os.Stderr, "asqs-core: post-generate format: %v (continuing)\n", err)
		}
	}

	// Phase 2 — evaluate the WHOLE project once: one compile + one test pass (+ optional E2E),
	// with a single fix loop across all generated files.
	fmt.Fprintf(os.Stderr, "asqs-core: evaluating %d generated test file(s) (whole-project compile + test)…\n", len(artifactPaths))
	evalRes, eerr := evaluator.RunEvaluation(ctx, sandbox, evaluator.EvalOptions{
		RepoPath:         repoAbs,
		Lang:             lang,
		MaxFixIterations: maxFix,
		// Per-step repair budgets, independent of the iteration budget above. Unset = maxFix.
		MaxCompileFixAttempts: cfg.Runner.MaxCompileFixAttempts,
		MaxTestFixAttempts:    cfg.Runner.MaxTestFixAttempts,
		ArtifactPaths:         artifactPaths,
		ExtendedArtifactPaths: extendedArtifactPaths,
		Fixer:                 fixer,
		// The fixer may now repair inherited breakage on evidence rather than on a regex guess
		// over each round's diagnostic.
		BaselineFailingPaths: append([]string(nil), baseline.Paths...),
		APISurfaceProvider:   apiSurface,
		// Keep the evaluator's view of the flag in step with the Fixer's, or the audit payload
		// (structured_user_message_config / _forced) contradicts what the fixer actually did.
		FixerStructuredUserMessage: cfg.Runner.FixerStructuredUserMessage,
		RunE2ETestPass:             anyE2E,
		CompileOncePerEval:         true,
		FormatAfterFix:             formatAfterFixHook,
		// Fix-loop bounds. The breaker thresholds were hardcoded, leaving an operator watching a
		// loop give up after three rounds with no lever at all.
		FixLoopRepeatStopThreshold:     cfg.Runner.FixLoopRepeatStopThreshold,
		FixLoopRecurrenceStopThreshold: cfg.Runner.FixLoopRecurrenceStopThreshold,
		FixLoopNoProgressStopThreshold: cfg.Runner.FixLoopNoProgressStopThreshold,
		FixContextRunesMax:             cfg.Runner.FixContextRunesMax,
		BackoffBetweenFixAttempts:      fixBackoffDuration(cfg.Runner.FixBackoff),
		// runner.disable_error_log_llm_summary was declared and documented but never passed on, so
		// the summariser below could not be turned off — another key the inert-field lint could not
		// see, because EvalOptions declares an identically named field.
		DisableErrorLogLLMSummary: cfg.Runner.DisableErrorLogLLMSummary,
		ErrorLogSummarizer:        errorLogSummarizer(cfg, fixerChat),
	}, audit)
	if eerr != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: evaluation error: %v\n", eerr)
	}
	sum.Iterations = evalRes.Iterations

	// Phase 3 — discard repeatedly-failing test files so the rest stay green. The evaluator flags
	// them (artifact-scoped) but never removes them; we do, but only when at least one generated
	// test still passes (stable-after-discard). Otherwise keep everything and report unstable.
	if evalRes.EarlyExitStableAfterDiscard && len(evalRes.EarlyExitDiscardPaths) > 0 {
		for _, p := range evalRes.EarlyExitDiscardPaths {
			np := normPath(p)
			if err := os.Remove(filepath.Join(repoAbs, filepath.FromSlash(np))); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "asqs-core: discard %s: %v\n", np, err)
			}
			sum.Discarded++
			if idx, ok := outcomeIdxByPath[np]; ok && idx < len(sum.Outcomes) {
				sum.Outcomes[idx].Discarded = true
				sum.Outcomes[idx].Err = "discarded: repeatedly failing"
			}
		}
		fmt.Fprintf(os.Stderr, "asqs-core: discarded %d repeatedly-failing test file(s); the rest are green.\n", sum.Discarded)
	}

	// The project is green when the eval passed outright, or stayed stable after discarding.
	sum.ProjectStable = evalRes.Stable || evalRes.EarlyExitStableAfterDiscard
	// A cancelled or timed-out evaluation never reports stable, and therefore never ships. Today
	// RunEvaluation cannot reach out.Stable = true on a cancelled context (every step failure
	// continues the loop, and sandboxStepFailure marks a cancelled step OK=false), so this only
	// makes the invariant explicit instead of emergent — which is what a ship gate warrants.
	// DeadlineExceeded as well as Canceled: RunEvaluation returns whatever ctx.Err() gives, and a
	// run killed by its own deadline has finished no more than an interrupted one.
	if eerr != nil && (errors.Is(eerr, context.Canceled) || errors.Is(eerr, context.DeadlineExceeded)) {
		sum.ProjectStable = false
	}

	// Close the loop the read half above opens: this run's failing output becomes the next run's
	// planning hint, so a repository that keeps failing the same way gets retrieval steered at the
	// failure instead of starting cold. A green run REMOVES the file — a hint describing a failure
	// that no longer exists is worse than none, since it localizes on code already fixed. Placed
	// after the discard pass so a run that went green by discarding writes no hint (CP36).
	persistLastEvalFailureHint(repoAbs, cfg, sum.ProjectStable, evalRes.StepResults)
	if sum.ProjectStable {
		sum.GapsStable = sum.GapsGenerated - sum.Discarded
		for i := range sum.Outcomes {
			if sum.Outcomes[i].Generated && !sum.Outcomes[i].Discarded {
				sum.Outcomes[i].Stable = true
			}
		}
	}

	// Terminal DB state: status + stable/iterations, plus the first-wave metrics that make this
	// run comparable in `asqs-core ab-report`. On an evaluation error the metrics stay NULL.
	stable := sum.ProjectStable
	iters := evalRes.Iterations
	completeRun(ctx, meta, runID, &stable, &iters, evalFirstWaveMetricsForDB(&evalRes, eerr, runUsage))
	return sum, nil
}

// docInsert is a pending per-symbol documentation insertion.
type docInsert struct {
	line    int    // 1-based declaration line (from the index)
	content string // the normalized doc comment block
	symbol  string // for logging
}

// applyCollectedDocInserts writes the collected per-symbol docs in one read/modify/write pass per
// file. It mirrors asqs-go's writeGeneratedDocFiles: resolve the insert above annotations, skip
// symbols that already have a doc (no duplicates), skip malformed comment blocks (no broken /** … */),
// then insert sorted-ascending with a running line offset so multiple docs in the same file land at
// the correct lines. Returns the number of docs inserted. Best-effort: every skip/failure is logged.
func applyCollectedDocInserts(repoAbs string, byFile map[string][]docInsert) int {
	applied := 0
	for relFile, inserts := range byFile {
		if strings.TrimSpace(relFile) == "" {
			continue
		}
		full := filepath.Join(repoAbs, filepath.FromSlash(relFile))
		b, err := os.ReadFile(full)
		if err != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: docs: read %s: %v\n", relFile, err)
			continue
		}
		s := string(b)
		lines := strings.Split(s, "\n")
		// Resolve/filter against the ORIGINAL file so the existing-doc and annotation checks are
		// consistent before any insertion shifts lines.
		var toApply []docInsert
		for _, in := range inserts {
			if in.line < 1 {
				continue
			}
			in.line = findInsertLineAboveAnnotations(lines, in.line)
			if !isWellFormedDocComment(in.content) {
				fmt.Fprintf(os.Stderr, "asqs-core: docs: skip %s (malformed doc block — not inserted)\n", in.symbol)
				continue
			}
			if hasExistingDocAbove(lines, in.line) {
				fmt.Fprintf(os.Stderr, "asqs-core: docs: skip %s (symbol already documented)\n", in.symbol)
				continue
			}
			toApply = append(toApply, in)
		}
		if len(toApply) == 0 {
			continue
		}
		sort.Slice(toApply, func(i, j int) bool { return toApply[i].line < toApply[j].line })
		lineOffset := 0
		for _, in := range toApply {
			s = insertContentAboveLine(s, in.line+lineOffset, in.content)
			lineOffset += strings.Count(in.content, "\n") + 1
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: docs: write %s: %v\n", relFile, err)
			continue
		}
		applied += len(toApply)
	}
	return applied
}

// insertContentAboveLine inserts content as new lines above the 1-based line in body (preserving newlines).
func insertContentAboveLine(body string, line int, content string) string {
	if line < 1 || content == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	if line > len(lines) {
		return body + "\n" + content
	}
	out := append(append(append([]string{}, lines[:line-1]...), strings.Split(content, "\n")...), lines[line-1:]...)
	return strings.Join(out, "\n")
}

// findInsertLineAboveAnnotations moves the insert line up past annotation lines (@Override, …) so the
// doc sits above all annotations on the declaration.
func findInsertLineAboveAnnotations(lines []string, declarationLine1Based int) int {
	insertLine := declarationLine1Based
	for insertLine > 1 {
		aboveIdx := insertLine - 2
		if aboveIdx < 0 || aboveIdx >= len(lines) || !isAnnotationLine(lines[aboveIdx]) {
			break
		}
		insertLine--
	}
	return insertLine
}

func isAnnotationLine(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && s[0] == '@'
}

// hasExistingDocAbove reports whether the symbol at insertLine1Based already has a doc comment
// immediately above it: a Javadoc/JSDoc/TSDoc block (/**, " * …", */), a C# /// XML doc, or //. Skips
// blank lines and stops at the first non-empty, non-doc line so unrelated far-away comments don't count.
func hasExistingDocAbove(lines []string, insertLine1Based int) bool {
	if insertLine1Based <= 1 {
		return false
	}
	startIdx := insertLine1Based - 2
	if startIdx < 0 {
		return false
	}
	const lookBack = 12
	endIdx := startIdx - lookBack
	if endIdx < 0 {
		endIdx = 0
	}
	for idx := startIdx; idx >= endIdx && idx < len(lines); idx-- {
		s := strings.TrimSpace(lines[idx])
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "*/") || strings.HasPrefix(s, "/**") || strings.HasPrefix(s, "///") || strings.HasPrefix(s, "//") {
			return true
		}
		if len(s) >= 2 && s[0] == '*' && (s[1] == ' ' || s[1] == '*' || s[1] == '/') {
			return true
		}
		break // first non-empty, non-doc line → no existing doc for this symbol
	}
	return false
}

// isWellFormedDocComment reports whether content is a well-formed in-file doc comment safe to write
// into source: a C# /// XML-doc run, or a /* … */ block comment scanned to reject the actual failure
// modes — a missing terminator (ends still open → swallows the following code), a stray/doubled */
// (closer with no open block), and trailing non-comment code after the block (only whitespace and //
// line comments may follow). Block comments do not nest. Malformed blocks would cause unfixable
// compilation errors, so they are skipped rather than inserted.
func isWellFormedDocComment(content string) bool {
	s := strings.TrimSpace(content)
	if s == "" {
		return false
	}
	// C# XML doc: every non-blank line starts with ///
	if strings.HasPrefix(s, "///") {
		for _, ln := range strings.Split(s, "\n") {
			if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "///") {
				return false
			}
		}
		return true
	}
	if !strings.HasPrefix(s, "/*") {
		return false
	}
	sawBlock, inside := false, false
	for i := 0; i < len(s); {
		if inside {
			if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
				inside, i = false, i+2
			} else {
				i++
			}
			continue
		}
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			sawBlock, inside, i = true, true, i+2
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' { // skip a // line comment
				i++
			}
		default: // stray */ or any other non-comment text
			return false
		}
	}
	return sawBlock && !inside
}

// --- helpers ----------------------------------------------------------------------------

func langCounts(files []indexer.FileVersion) (nJava, nJST, nCSharp int) {
	for _, f := range files {
		switch f.Lang {
		case "java":
			nJava++
		case "javascript", "typescript":
			nJST++
		case "csharp":
			nCSharp++
		}
	}
	return
}

func detectPrimaryLang(nJava, nJST, nCSharp int) string {
	switch {
	case nJST >= nJava && nJST >= nCSharp && nJST > 0:
		return "typescript"
	case nJava >= nCSharp && nJava > 0:
		return "java"
	case nCSharp > 0:
		return "csharp"
	default:
		return ""
	}
}

// buildLangIndexer selects the language-native indexer for the primary language. Mono-repo /
// multi-language merging from the proprietary product is intentionally omitted.
func buildLangIndexer(ctx context.Context, cfg *config.Config, repoAbs, lang string, nJava, nCSharp, nJST int) (indexer.LangIndexer, map[string]struct{}, error) {
	switch lang {
	case "javascript", "typescript":
		if strings.TrimSpace(cfg.Indexer.JSTIndexerPath) == "" {
			return nil, nil, fmt.Errorf("indexer.jsts.indexer_path is not set (build tools/js-ts-indexer and point config at dist/index.js)")
		}
		parsed, _, err := jstindexer.RunIndexer(ctx, repoAbs, cfg.Indexer.JSTIndexerPath, jstindexerRunConfig(cfg, 0))
		if err != nil {
			return nil, nil, err
		}
		parsed = indexer.FilterParsedMapBySkipPrefixes(parsed, cfg.Indexer.SkipPathPrefixes)
		return indexer.LangIndexerFromMap(parsed), indexer.IndexablePathsFromParsedMap(parsed), nil
	case "csharp":
		dll := strings.TrimSpace(cfg.Indexer.CSharpIndexerDllPath)
		if dll == "" {
			return nil, nil, fmt.Errorf("indexer.csharp.indexer_dll_path is not set (dotnet publish tools/csharp-indexer)")
		}
		parsed, err := csharpindexer.Run(ctx, repoAbs, dll, csharpindexerRunConfig(cfg, 0))
		if err != nil {
			return nil, nil, err
		}
		parsed = indexer.FilterParsedMapBySkipPrefixes(parsed, cfg.Indexer.SkipPathPrefixes)
		indexer.AddJavaParsedMapPathAliases(parsed)
		return javaAdvancedLangIndexer(parsed), indexer.IndexablePathsFromParsedMap(parsed), nil
	case "java":
		if strings.EqualFold(strings.TrimSpace(cfg.Indexer.Type), "advanced") && strings.TrimSpace(cfg.Indexer.AdvancedJarPath) != "" {
			parsed, err := javaindexer.RunJAR(ctx, repoAbs, cfg.Indexer.AdvancedJarPath, javaindexerRunJARConfig(cfg, 0))
			if err != nil {
				return nil, nil, err
			}
			parsed = indexer.FilterParsedMapBySkipPrefixes(parsed, cfg.Indexer.SkipPathPrefixes)
			indexer.AddJavaParsedMapPathAliases(parsed)
			return javaAdvancedLangIndexer(parsed), indexer.IndexablePathsFromParsedMap(parsed), nil
		}
		// Minimal heuristic Java indexer (no JAR required).
		return indexer.LangIndexer(javaindexer.Index), nil, nil
	}
	return nil, nil, fmt.Errorf("unsupported language %q", lang)
}

func writeArtifact(repoAbs, relPath, content string) error {
	full := filepath.Join(repoAbs, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// normPath normalizes a repo-relative path (clean, forward slashes, no leading slash) so artifact
// paths from generation and the evaluator's discard list compare equal. It mirrors the evaluator's
// own normalizePathForFix (TrimSpace → backslash→slash → Clean → ToSlash → trim leading slash) so a
// path in EarlyExitDiscardPaths reliably keys back into the per-gap outcome map.
func normPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
}

func embedProvider(cfg *config.Config) string {
	if p := strings.TrimSpace(cfg.LLM.EmbeddingProvider); p != "" {
		return p
	}
	return strings.TrimSpace(cfg.LLM.Provider)
}

func embedModel(cfg *config.Config) string { return strings.TrimSpace(cfg.LLM.EmbeddingModel) }

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func planItemSymbol(item *retrieval.TestPlanItem) string {
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		return item.Gap.Symbol.FQName
	}
	return ""
}

func isE2E(item *retrieval.TestPlanItem) bool {
	return item != nil && strings.EqualFold(strings.TrimSpace(item.Layer), "e2e")
}

// projectIntelForGap returns the project-intel markdown for one gap: candidates re-ranked against
// the gap's target embedding, with a boost for documents that explicitly NAME the target symbol or
// its enclosing type (matched on FQ name — symbol ids churn on every reindex). Falls back to the
// run-wide snapshot when candidates are absent (cache formats predating them) or the gap has no
// embedded target chunk.
func projectIntelForGap(pi *projectintel.Result, piCfg config.ProjectIntelConfig, item *retrieval.TestPlanItem) string {
	if pi == nil {
		return ""
	}
	fallback := strings.TrimSpace(pi.Snapshot.Markdown)
	if len(pi.Candidates) == 0 || item == nil {
		return fallback
	}
	var targetEmbedding []float32
	if item.Context != nil && item.Context.TargetMethod != nil && item.Context.TargetMethod.Chunk != nil {
		targetEmbedding = item.Context.TargetMethod.Chunk.Embedding
	}
	// A document that explicitly names the target symbol beats one that merely embeds similarly.
	var boost projectintel.SymbolLinkBoost
	if item.Gap != nil && item.Gap.Symbol != nil {
		boost.TargetFQName = item.Gap.Symbol.FQName
	}
	if item.Context != nil && item.Context.TargetClass != nil && item.Context.TargetClass.Symbol != nil {
		boost.ContainerFQName = item.Context.TargetClass.Symbol.FQName
	}
	return projectintel.SelectForGapWithLinks(pi.Candidates, targetEmbedding,
		piCfg.EffectiveMaxDocFiles(), piCfg.EffectiveMaxSkillFiles(), piCfg.EffectiveMaxTotalRunes(),
		fallback, boost)
}

// defaultEmbeddingCacheRetentionDays is used when llm.embedding_cache_retention_days is unset.
const defaultEmbeddingCacheRetentionDays = 30

// pruneEmbeddingCache removes cache rows unused for longer than the configured retention.
// Best-effort by design: the cache is an optimization, and a failure to prune must not fail a run.
func pruneEmbeddingCache(store *embeddings.Store, retentionDays int) {
	if store == nil {
		return
	}
	if retentionDays <= 0 {
		retentionDays = defaultEmbeddingCacheRetentionDays
	}
	n, err := store.PruneEmbeddingCache(context.Background(), time.Duration(retentionDays)*24*time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: embedding cache prune: %v (continuing)\n", err)
		return
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "asqs-core: embedding cache: pruned %d row(s) unused for %d day(s)\n", n, retentionDays)
	}
}

// buildPlanOptions maps config onto retrieval.PlanOptions — the config-reading half CP17 exists
// for: these keys parsed, validated and documented while the planner ran on built-in defaults.
//
// The section budgets (max_similar_tests, max_dependency_chunks, max_fixtures) and the MMR lambda
// are deliberately LEFT AT ZERO here: retrieval substitutes its Default* constants for a zero,
// which is exactly what every shipped config resolved to, and upstream's config restructure froze
// those keys pending an A/B. Wiring them now would promote unmeasured defaults (rule 10).
func buildPlanOptions(cfg *config.Config, workflowLang, repoID string) retrieval.PlanOptions {
	lang := strings.TrimSpace(workflowLang)
	if lang == "" {
		lang = "java"
	}
	if cfg == nil {
		return retrieval.PlanOptions{Lang: lang, RepoID: repoID}
	}
	p := retrieval.PlanOptions{
		Lang:                      lang,
		RepoID:                    repoID,
		MaxGaps:                   cfg.Indexer.MaxGaps,
		MaxGapsPerFile:            cfg.Indexer.MaxGapsPerFile,
		MaxGapsE2E:                cfg.Indexer.MaxGapsE2E,
		MaxGapsPerFileE2E:         cfg.Indexer.MaxGapsPerFileE2E,
		RetrievalProfileE2E:       defaultRetrievalProfileE2E(cfg, lang),
		CriticalModulePrefixes:    cfg.Indexer.CriticalModulePrefixes,
		SkipPathPrefixes:          cfg.Indexer.SkipPathPrefixes,
		DependencyMaxDepth:        cfg.Retrieval.DependencyMaxDepth,
		Fusion:                    cfg.Retrieval.Fusion,
		ProfileBudgets:            retrieval.NormalizeProfileBudgetsMap(cfg.Retrieval.ProfileBudgets),
		RetrievalProfile:          cfg.Retrieval.Profile,
		FailureHintFile:           strings.TrimSpace(cfg.Retrieval.FailureHintFile),
		DisableHybridModuleFilter: cfg.Retrieval.DisableHybridModuleFilter,
	}
	if m, err := workspace.NormalizeMonoRepoWorkspace(cfg.Indexer.MonoRepoWorkspace); err == nil && m != "" {
		p.MonoRepoGapPrefix = m
	}
	applyRetrievalAbstentionDefaults(&cfg.Retrieval, &p)
	return p
}

// defaultRetrievalProfileE2E resolves the E2E retrieval profile: explicit profile_e2e, else the
// unit profile, else a language default (http_api for backends, e2e_playwright otherwise).
func defaultRetrievalProfileE2E(cfg *config.Config, workflowLang string) string {
	if cfg == nil {
		return string(retrieval.ProfileE2EPlaywright)
	}
	if s := strings.TrimSpace(cfg.Retrieval.ProfileE2E); s != "" {
		return s
	}
	if s := strings.TrimSpace(cfg.Retrieval.Profile); s != "" {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(workflowLang)) {
	case "java", "csharp", "cs":
		return string(retrieval.ProfileHTTPAPI)
	default:
		return string(retrieval.ProfileE2EPlaywright)
	}
}

// applyRetrievalAbstentionDefaults sets PlanOptions sufficiency fields from config.
// When abstention_disabled is false (default), zero YAML/env values become meaningful defaults
// (at least one similar-reference chunk; cosine ≥ 0.5 when target has an embedding).
func applyRetrievalAbstentionDefaults(rc *config.RetrievalConfig, p *retrieval.PlanOptions) {
	if rc == nil || p == nil {
		return
	}
	if rc.AbstentionDisabled {
		p.MinSimilarTestsForGeneration = 0
		p.MinSimilarityCosine = 0
		return
	}
	switch {
	case rc.MinSimilarTestsForGeneration == -1:
		p.MinSimilarTestsForGeneration = 0
	case rc.MinSimilarTestsForGeneration <= 0:
		p.MinSimilarTestsForGeneration = retrieval.DefaultAbstentionMinSimilarTests
	default:
		p.MinSimilarTestsForGeneration = rc.MinSimilarTestsForGeneration
	}
	if rc.MinSimilarityCosine < 0 {
		p.MinSimilarityCosine = 0
	} else if rc.MinSimilarityCosine == 0 {
		p.MinSimilarityCosine = retrieval.DefaultAbstentionMinSimilarityCosine
	} else {
		p.MinSimilarityCosine = rc.MinSimilarityCosine
	}
}

// applyRetrievalContextCompactToFormat copies retrieval.context_compact into
// FormatOptions.ContextCompact. Everything except the on/off switch is frozen: the rune caps fall
// through to retrieval's DefaultCompact* constants at zero, and the merge/dedupe behaviours stay
// off until an A/B earns them a different default.
func applyRetrievalContextCompactToFormat(rc *config.RetrievalConfig, fo *retrieval.FormatOptions) {
	if rc == nil || fo == nil {
		return
	}
	enabled := true
	if rc.ContextCompact.Enabled != nil {
		enabled = *rc.ContextCompact.Enabled
	}
	fo.ContextCompact = retrieval.ContextCompactOptions{Enabled: enabled}
}

// compactPlanContexts applies retrieval.CompactRetrievalContext to every plan item's context.
//
// This is the production call site for context compaction. Before it existed, the whole mechanism
// — the deterministic merging/deduping/truncation, its tests, and the full YAML plumbing through
// config.ContextCompactConfig — had ZERO production callers: FormatOptions.ContextCompact was
// written and never read, so tuning retrieval.context_compact did nothing at all.
//
// It runs once per plan, after the unit and E2E plans are merged and before the generation
// fan-out, so the cost is paid once per item, test generation and the doc pass see the same
// compacted context, and the audit counters describe the whole run. The compaction never touches
// the target method, the target class, or the failure hint — see context_compact.go.
func compactPlanContexts(ctx context.Context, formatOpts retrieval.FormatOptions, audit interface {
	Log(ctx context.Context, step string, payload interface{})
}, plan *retrieval.TestPlan) {
	if plan == nil || len(plan.Items) == 0 {
		return
	}
	compactOpts := formatOpts.ContextCompact
	if !compactOpts.Enabled {
		return
	}
	var (
		items          int
		runesBefore    int64
		runesAfter     int64
		depsMerged     int
		headersDeduped int
		chunksTrimmed  int
	)
	for _, item := range plan.Items {
		if item == nil || item.Context == nil {
			continue
		}
		stats := retrieval.CompactRetrievalContext(item.Context, compactOpts)
		items++
		runesBefore += stats.InputContentRunes
		runesAfter += stats.OutputContentRunes
		depsMerged += stats.MergedDependencyFileGroups
		headersDeduped += stats.DedupedBoilerplateChunks
		chunksTrimmed += stats.TruncatedChunks
	}
	if audit != nil && items > 0 {
		saved := runesBefore - runesAfter
		var pct int64
		if runesBefore > 0 {
			pct = saved * 100 / runesBefore
		}
		audit.Log(ctx, "retrieve.context_compacted_total", map[string]interface{}{
			"message":          "Compacted retrieval context before generation.",
			"items":            items,
			"runes_before":     runesBefore,
			"runes_after":      runesAfter,
			"runes_saved":      saved,
			"percent_saved":    pct,
			"chunks_merged":    depsMerged,
			"headers_deduped":  headersDeduped,
			"chunks_truncated": chunksTrimmed,
		})
	}
}
