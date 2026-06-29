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
	"github.com/asqs/asqs-core/internal/evaluator/llmfix"
	"github.com/asqs/asqs-core/internal/generator"
	"github.com/asqs/asqs-core/internal/generator/contract"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/projectintel"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/llm"
	"github.com/asqs/asqs-core/internal/overview"
	"github.com/asqs/asqs-core/internal/runner"
	"github.com/asqs/asqs-core/internal/testbootstrap"
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

// stdoutAuditor satisfies the (identical) Auditor interface declared by indexer / retrieval /
// evaluator. It prints a compact line per step to stderr.
type stdoutAuditor struct{}

func (stdoutAuditor) Log(_ context.Context, step string, _ interface{}) {
	fmt.Fprintf(os.Stderr, "  · %s\n", step)
}
func (stdoutAuditor) LogError(_ context.Context, step string, payload interface{}) {
	fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", step, payload)
}

// Run executes the pipeline against opts.RepoPath (already a local working tree).
func Run(ctx context.Context, cfg *config.Config, opts Options) (Summary, error) {
	var sum Summary
	audit := stdoutAuditor{}
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
	chat, err := llm.NewChatCompleter(cfg)
	if err != nil {
		return sum, fmt.Errorf("llm chat client: %w", err)
	}
	embedder, err := llm.NewEmbedder(cfg)
	if err != nil {
		return sum, fmt.Errorf("llm embedder: %w", err)
	}
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
			fmt.Fprintf(os.Stderr, "asqs-core: e2e bootstrap: %v (continuing)\n", err)
		}
	}

	// --- Index --------------------------------------------------------------------------
	langIdx, indexable, err := buildLangIndexer(ctx, cfg, repoAbs, lang, nJava, nCSharp, nJST)
	if err != nil {
		return sum, fmt.Errorf("language indexer: %w", err)
	}
	runID := fmt.Sprintf("core_%d", time.Now().UnixNano())
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
	}); err != nil {
		return sum, fmt.Errorf("index: %w", err)
	}

	// --- Plan ---------------------------------------------------------------------------
	planOpts := retrieval.PlanOptions{
		Lang:       lang,
		RepoID:     opts.RepoID,
		MaxGaps:    orDefault(opts.MaxGaps, 10),
		MaxGapsE2E: opts.MaxGapsE2E,
		Audit:      audit,
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
			RepoAbs:           repoAbs,
			Lang:              lang,
			CurrentFiles:      files,
			ConfigFingerprint: piCfg.ConfigFingerprintHash(),
			LLM:               chat,
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
				ForceRefresh:        piCfg.ForceRefresh,
				FingerprintMode:     piCfg.EffectiveFingerprintMode(),
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
	rules := contract.ByLang(lang)
	gen := &generator.LLMGenerator{
		LLM:                    chat,
		ContractRules:          &rules,
		TwoPhaseTestGeneration: cfg.Runner.TwoPhaseTestGeneration,
		RepoPath:               repoAbs,
	}
	fixer := &llmfix.Fixer{LLM: chat}
	sandbox := runner.NewSandboxFromConfig(cfg)
	maxFix := orDefault(cfg.Runner.StartMaxIteration, 3)

	// Formatting (matches asqs-go): format generated tests post-generate and after each LLM fix so
	// they satisfy the repo's style gates (e.g. `dotnet format --verify-no-changes`, .editorconfig
	// treated as errors, analyzers). C# defaults to `dotnet format` when runner.format_command is empty.
	formatCmd := runner.EffectivePostGenerateFormatCommand(lang, cfg.Runner.FormatCommand)
	formatTimeout := 2 * time.Minute
	if d, derr := time.ParseDuration(cfg.Runner.Timeout); derr == nil && d > 0 {
		formatTimeout = d
	}
	var formatAfterFixHook func(context.Context, string, []string) error
	if formatCmd != "" {
		formatAfterFixHook = func(ctx context.Context, repoPath string, updatedPaths []string) error {
			err := runner.FormatAfterFixForSandbox(sandbox, ctx, repoPath, lang, formatCmd, cfg.Runner.FormatOnlyAdded, updatedPaths, formatTimeout)
			if err != nil && errors.Is(err, runner.ErrFormatSkippedNoDotnet) {
				return fmt.Errorf("%w: %v", evaluator.ErrFormatAfterFixSkipped, err)
			}
			return err
		}
	}
	var docGen *generator.LLMDocGenerator
	var docFmt retrieval.FormatOptions
	if opts.GenerateDocs {
		docGen = &generator.LLMDocGenerator{LLM: chat}
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
	if opts.GenerateDocs {
		og := &overview.LLMOverviewDocGenerator{
			LLM:                     chat,
			Path:                    strings.TrimSpace(cfg.Indexer.OverviewDocPath),
			MaxCompletionTokensFull: cfg.Indexer.OverviewMaxCompletionTokens,
		}
		overviewWG.Add(1)
		go func() {
			defer overviewWG.Done()
			fmt.Fprintln(os.Stderr, "asqs-core: generating overview documentation (in parallel)…")
			overviewContent, overviewPath, overviewErr = og.Generate(ctx, meta, lang, repoAbs, cfg.Indexer.OverviewMaxFilesPerSlice, cfg.Indexer.OverviewMaxIndexRunesPerSlice)
		}()
	}

	// Phase 1 — generate + write every gap's test (no per-gap evaluation). Collect the unique
	// artifact paths so the whole project is compiled/tested exactly once below.
	var artifactPaths []string
	seen := map[string]bool{}
	outcomeIdxByPath := map[string]int{} // normalized path -> index of the gap that first wrote it
	anyE2E := false
	docInsertsByFile := map[string][]docInsert{} // collected per-symbol docs, applied per file after the loop
	for _, item := range plan.Items {
		out := GapOutcome{Symbol: planItemSymbol(item)}
		ctxStr := retrieval.BuildLLMContextForGap(item, formatOpts)
		if piResult != nil {
			if piMarkdown := strings.TrimSpace(piResult.Snapshot.Markdown); piMarkdown != "" {
				ctxStr = piMarkdown + "\n\n" + ctxStr
			}
		}
		content, relPath, gerr := gen.Generate(ctx, item, ctxStr)
		switch {
		case gerr != nil:
			out.Err = gerr.Error()
		case strings.TrimSpace(content) == "" || strings.TrimSpace(relPath) == "":
			out.Err = "empty generation"
		default:
			if werr := writeArtifact(repoAbs, relPath, content); werr != nil {
				out.Err = "write: " + werr.Error()
			} else {
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
		fmt.Fprintln(os.Stderr, "asqs-core: no test files were generated — skipping evaluation.")
		return sum, nil
	}

	// Post-generate format: format the freshly written test files before evaluation so a style gate
	// (dotnet format --verify-no-changes, .editorconfig-as-errors, analyzers) doesn't fail on layout.
	// Best-effort: a formatter problem is logged but never aborts the run.
	if formatCmd != "" {
		fmt.Fprintf(os.Stderr, "asqs-core: formatting %d generated file(s) (%s)…\n", len(artifactPaths), formatCmd)
		if err := runner.FormatAfterFixForSandbox(sandbox, ctx, repoAbs, lang, formatCmd, cfg.Runner.FormatOnlyAdded, artifactPaths, formatTimeout); err != nil && !errors.Is(err, runner.ErrFormatSkippedNoDotnet) {
			fmt.Fprintf(os.Stderr, "asqs-core: post-generate format: %v (continuing)\n", err)
		}
	}

	// Phase 2 — evaluate the WHOLE project once: one compile + one test pass (+ optional E2E),
	// with a single fix loop across all generated files.
	fmt.Fprintf(os.Stderr, "asqs-core: evaluating %d generated test file(s) (whole-project compile + test)…\n", len(artifactPaths))
	evalRes, eerr := evaluator.RunEvaluation(ctx, sandbox, evaluator.EvalOptions{
		RepoPath:           repoAbs,
		Lang:               lang,
		MaxFixIterations:   maxFix,
		ArtifactPaths:      artifactPaths,
		Fixer:              fixer,
		RunE2ETestPass:     anyE2E,
		CompileOncePerEval: true,
		FormatAfterFix:     formatAfterFixHook,
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
	if sum.ProjectStable {
		sum.GapsStable = sum.GapsGenerated - sum.Discarded
		for i := range sum.Outcomes {
			if sum.Outcomes[i].Generated && !sum.Outcomes[i].Discarded {
				sum.Outcomes[i].Stable = true
			}
		}
	}
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
			return nil, nil, fmt.Errorf("indexer.jst_indexer_path is not set (build tools/js-ts-indexer and point config at dist/index.js)")
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
			return nil, nil, fmt.Errorf("indexer.csharp_indexer_dll_path is not set (dotnet publish tools/csharp-indexer)")
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
