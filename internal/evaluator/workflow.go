package evaluator

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/evaluator/errclass"
	"github.com/asqs/asqs-core/internal/evaluator/errout"
	"github.com/asqs/asqs-core/internal/evaluator/fixslice"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// nowFunc is the wall-clock source for sandbox-step duration measurement (A.6 — tool attempt
// durations). It is a package-level seam so tests can inject a deterministic clock without
// depending on real elapsed time. Callers that overwrite this for a test MUST restore it (use
// the tests' helper withFakeNow). Production: time.Now (UTC normalisation handled in
// stampDuration so callers never rely on the seam returning UTC).
var nowFunc = time.Now

// maxFixerErrorCitedPaths is the count cap for dependency/source files the workflow injects into
// the fixer prompt purely because they were cited by the error log (stack traces, javac FQCN
// hints). It is a small superset of the previous 24-path cap so the pet-clinic-style case where
// the compiler names ~6 classes in two diagnostics fits comfortably; the per-prompt size is then
// bounded by maxFixerDependencyContextRunes.
const maxFixerErrorCitedPaths = 48

// maxFixerDependencyContextRunes is a soft budget on the cumulative rune count of FixRequest.Files
// when filling the error-cited tail. Artifacts and ArtifactDependencies are always pulled first and
// may exceed this budget on their own; the budget only stops us from extending the tail with more
// error-cited sources once we're already over. A runes budget (as opposed to bytes) keeps the
// semantics predictable across multibyte test fixtures.
const maxFixerDependencyContextRunes = 120000

// maxMissingTypeFilesPerFix bounds how many source files the fixer pulls in for type names the compiler
// could not resolve (RC1: errout.ResolveMissingTypeFiles). A failing test rarely references more than a
// handful of unknown types; the cap keeps a pathological error log from dragging the whole domain model
// into the prompt. These files are also subject to the shared maxFixerDependencyContextRunes budget.
const maxMissingTypeFilesPerFix = 8

// auditErrorOutputMaxRunes caps deterministic audit storage before relying on LLM summary alone.
const auditErrorOutputMaxRunes = 48000

// errorLogLLMSummaryMinRunes triggers optional LLM summarization for evaluator.fix_request payloads when canonical output exceeds this size.
const errorLogLLMSummaryMinRunes = 8000

// ErrFormatAfterFixSkipped is returned (wrapped with fmt.Errorf("%w", ...)) when FormatAfterFix did not run
// on the host because a formatter such as dotnet was not on PATH (local sandbox only; docker sandboxes run format in the toolchain container).
var ErrFormatAfterFixSkipped = errors.New("format after fix skipped (formatter not on PATH)")

// EvalOptions configures the evaluation workflow (which steps to run, max fix iterations).
type EvalOptions struct {
	RepoPath             string              // repo root for reading/writing artifact files
	Lang                 string              // e.g. "java"
	CriticalModules      []string            // for optional mutation step
	RunMutation          bool                // if true, run mutation tests for critical modules
	MaxFixIterations     int                 // max loop iterations (compile fail → fix; test fail → fix; then re-run)
	ArtifactPaths        []string            // repo-relative paths to generated files (read and passed to Fixer on failure)
	ArtifactDependencies map[string][]string // optional: artifact path -> repo-relative paths of source/dependency files to include in fix context (e.g. controller, repository, service)
	// ArtifactContexts: optional artifact path -> retrieval/generation context string that was used to create the test.
	// Passed to the fixer so repairs preserve dependency-graph intent and branch-gap guidance from planning.
	ArtifactContexts map[string]string
	// FailingTestCandidatePaths: when set, test failure output is parsed for failing paths among these candidates (generated + existing test paths). Ensures pre-existing failing tests (e.g. port/config) are included in fix context. Optional.
	FailingTestCandidatePaths []string
	Fixer                     Fixer // optional: when set, compile/test failures are sent to LLM for fix; applied and re-run
	// FormatAfterFix runs after applying an LLM fix. The third argument is repo-relative paths just written (e.g. fixed tests); used when format_only_added limits formatting to those files.
	FormatAfterFix func(context.Context, string, []string) error
	// TestFramework is the detected test framework (e.g. "jest", "junit"). Passed to Fixer for better fixes.
	TestFramework string
	// BuildTool, CompileCommand, TestCommand are passed to Fixer so the LLM knows the exact build/test commands.
	BuildTool      string
	CompileCommand string
	TestCommand    string
	// CompileOncePerEval when true: after compile succeeds once in this RunEvaluation, skip runner.Compile on later
	// fix-loop iterations (test, lint, coverage still run each time). Use when compile is expensive and only needed
	// once per eval (e.g. npm ci). Default false = compile runs every iteration.
	CompileOncePerEval bool
	// UnitTestCommand overrides the first (unit) test pass when non-empty; otherwise TestCommand is used for the unit pass.
	UnitTestCommand string
	// E2ETestCommand overrides the second test pass when RunE2ETestPass is true; empty = infer from E2EFramework + Lang + RepoPath/BuildTool.
	E2ETestCommand string
	// E2EFramework: JS/TS playwright|cypress; Java playwright-java|selenium|selenide; C# playwright-dotnet|selenium.
	E2EFramework string
	// RunE2ETestPass when true: after the unit test step succeeds, run a second test step (JS/TS, Java, C# when enabled).
	RunE2ETestPass bool
	// RepeatedTestFailureThreshold: after this many consecutive evaluation iterations with the same failing generated test fingerprint (unit or E2E), stop the fix loop early. 0 = default 5; negative = disabled. See EvalWorkflowResult.EarlyExitDiscardPaths.
	RepeatedTestFailureThreshold int
	// MonoRepoWorkspace is a normalized repo-relative prefix when the run is scoped to a mono-repo subdirectory; used to locate pom.xml/package.json/.csproj for fixer manifests.
	MonoRepoWorkspace string
	// AbortOnUnrecoverableEnvCompileFailure, when true, aborts the fix loop early once the compile step
	// fails on an environmental issue that is clearly outside the generated artifact's scope AND a
	// scoped-compile retry has already been attempted (typically: private NuGet feeds requiring credentials
	// the build container doesn't have). Prevents burning every MaxFixIterations on a condition that no
	// additional fix attempt can change. Default false preserves existing behaviour for unknown envs;
	// callers that run in credential-limited CI (where the condition will recur identically across
	// iterations) should enable it. Surfaces as audit event evaluator.compile_unrecoverable_environment_failure.
	AbortOnUnrecoverableEnvCompileFailure bool
	// FixerDependencySignatureOnly (Phase 3 opt-in) when true, dependency/source files the fixer
	// receives purely as read-only context — i.e. not in opts.ArtifactPaths, not declared in
	// ArtifactDependencies for any artifact, and not cited in the sanitized error output — are
	// sliced to signatures only via internal/evaluator/fixslice before being shipped to the LLM.
	// Artifacts, their declared dependencies, and error-cited sources are always sent in full.
	// Default false = every file keeps its full body (existing behaviour). Runner flag:
	// `runner.fixer_dependency_signature_only`; env RUNNER_FIXER_DEPENDENCY_SIGNATURE_ONLY.
	FixerDependencySignatureOnly bool
	// FixerStructuredUserMessage (Phase 3 opt-in) when true, the fixer emits its user message as
	// tagged `<error>` / `<file role=… writable=…>` XML-like blocks instead of the legacy
	// `--- path ---` layout, giving the model explicit section boundaries. Default false keeps
	// the legacy layout. Runner flag: `runner.fixer_structured_user_message`; env
	// RUNNER_FIXER_STRUCTURED_USER_MESSAGE.
	FixerStructuredUserMessage bool
	// SkipFixerOnInfrastructureFailure when true skips LLM fix attempts when errclass classifies the test failure as infrastructure/environment (missing DB, invalid connection string). Default false.
	SkipFixerOnInfrastructureFailure bool
	// DisableErrorLogLLMSummary when true disables LLM summarization of large error logs in evaluator.fix_request audit payloads. Default false = summarization enabled when ErrorLogSummarizer is set.
	DisableErrorLogLLMSummary bool
	// ErrorLogSummarizer optionally summarizes large canonical error text for audit rows (typically wired from the fixer ChatCompleter).
	ErrorLogSummarizer func(context.Context, string) (string, error)
	// GapSessionID scopes multi-turn fixer conversation when multiple gaps run in parallel.
	GapSessionID string
}

// DefaultEvalOptions returns options with sensible defaults (e.g. MaxFixIterations = 3).
func DefaultEvalOptions(repoPath, lang string) EvalOptions {
	return EvalOptions{
		RepoPath:         repoPath,
		Lang:             lang,
		MaxFixIterations: 3,
		RunMutation:      false,
	}
}

// EvalWorkflowResult is the result of running the full evaluation workflow (all steps + fix loop).
type EvalWorkflowResult struct {
	Stable          bool         // true if evaluation passed without needing fixes
	Iterations      int          // number of fix-loop iterations
	StepResults     []StepResult // last run of each step (compile, test, lint, coverage, mutation)
	LastFixAction   FixAction    // last recommended fix (if failed)
	ArtifactResults []EvalResult // per-artifact results when running for specific generated files

	// IterationArtifacts records, for each fix-loop iteration in which the LLM fixer actually
	// wrote at least one file, the repo-relative paths it wrote. Iterations where no fix was
	// applied (no fixer configured, fix rejected as low-value, or step passed without a fix)
	// produce no entry, so len(IterationArtifacts) <= Iterations.
	//
	// Consumers (notably session/engine.populateFromEvaluate, A.5 — per-gap iteration tracking)
	// use this to compute gap_sessions.iterations_used: for each gap, count entries whose
	// touched-paths set intersects the gap's ArtifactPaths. Iterations that touched no file
	// cannot be attributed to any specific gap and so are correctly excluded from per-gap counts.
	IterationArtifacts [][]string

	// First-wave quality metrics (audit / observability; see docs/DOCUMENTATION.md — First-wave quality metrics).
	// CompileOKAfterGenerate: on the first evaluation loop iteration, the compile step succeeded (first compile after artifacts were written).
	CompileOKAfterGenerate bool
	// TestOKWithoutFix: evaluation became stable without any LLM-driven compile or test repair (CompileFixCount==0 && TestFixCount==0).
	// Aligns with pass@1-style "no repair" reporting (Chen et al. HumanEval; Hendrycks et al. APPS).
	TestOKWithoutFix bool
	CompileFixCount  int // LLM fix invocations for StepCompile
	TestFixCount     int // LLM fix invocations for StepTest or StepTestE2E (shared budget in loop)

	// EarlyExitDiscardPaths: set when the fix loop stopped early because the same generated tests failed repeatedly (RepeatedTestFailureThreshold). Orchestrator applies discards like max-iteration unstable handling.
	EarlyExitDiscardPaths []string
	// EarlyExitStableAfterDiscard: true when at least one other generated artifact was not among failing paths — after discarding failing files, the run is treated as stable (ship kept tests). False when every generated artifact appears failing — unstable + reschedule when configured.
	EarlyExitStableAfterDiscard bool
}

// RunEvaluation runs the evaluation workflow in the sandbox: build/compile → test → lint → coverage → (optional) mutation.
// When compile or test fails and Fixer is set, the error and code are sent to the Fixer (LLM); fixes are applied and the step is re-run.
// Compile and test fixes are each tried at most MaxFixIterations times (same as the outer loop budget; 0 = default 3).
// When opts.CompileOncePerEval is true, after the first successful compile in this invocation, later iterations skip Compile but still run Test (and following steps) each time.
// Runner must not be nil. Audit is optional.
func RunEvaluation(ctx context.Context, runner SandboxRunner, opts EvalOptions, audit Auditor) (EvalWorkflowResult, error) {
	if runner == nil {
		return EvalWorkflowResult{}, fmt.Errorf("evaluator: SandboxRunner required")
	}
	out := EvalWorkflowResult{
		StepResults: make([]StepResult, 0, 5),
	}
	if opts.MaxFixIterations <= 0 {
		opts.MaxFixIterations = 3
	}
	// Multi-turn fixers (e.g. llmfix.Fixer) may retain chat history across Fix calls; clear at each evaluation run boundary.
	if opts.Fixer != nil {
		if r, ok := opts.Fixer.(interface{ ResetConversation() }); ok {
			r.ResetConversation()
		}
	}
	// Use current iteration budget (MaxFixIterations) for both compile and test fix limits.
	maxCompileFix := opts.MaxFixIterations
	maxTestFix := opts.MaxFixIterations
	var compileFixAttempts, testFixAttempts int
	// Per-step fix-loop repeat detectors. Each applyLLMFix call canonicalises its input into a
	// fingerprint (step + sorted artifact paths + sanitised error output) and increments the
	// streak when it matches the previous call; at FixLoopRepeatStopThreshold consecutive matches
	// the circuit-breaker aborts further fix attempts for that step and emits
	// evaluator.fix_rejected_low_value with reason="fix_loop_repeat" so operators see the
	// saturation point rather than burning the full attempt budget on the same broken prompt.
	var compileFixState, testFixState FixLoopState
	var compileSucceededThisEval bool
	var firstIterCompileOK bool
	firstIterCompileRecorded := false
	// scopedCompileTried ensures the NU*-triggered scoped-compile fallback runs at most once per evaluation.
	// It is intentionally loop-scoped (not reset across iterations) because after a successful scoped build the
	// scoped test command is promoted (opts.UnitTestCommand) and subsequent iterations must not silently revert
	// to the full-sln command.
	var scopedCompileTried bool
	repThr := effectiveRepeatedTestFailureThreshold(opts)
	var unitFailStreak int
	var unitFailFP string
	var e2eFailStreak int
	var e2eFailFP string

	for iter := 0; iter < opts.MaxFixIterations; iter++ {
		out.Iterations = iter + 1
		if audit != nil {
			audit.Log(ctx, "evaluator.iteration", map[string]interface{}{
				"message":   fmt.Sprintf("Evaluation iteration %d of %d.", out.Iterations, opts.MaxFixIterations),
				"iteration": out.Iterations, "max": opts.MaxFixIterations,
			})
		}

		// ----- Step 1: Compile -----
		var compileRes StepResult
		if opts.CompileOncePerEval && compileSucceededThisEval {
			compileRes = StepResult{
				Step: StepCompile, OK: true,
				Summary: "skipped (compile_once_per_eval: already succeeded this evaluation)",
			}
			if audit != nil {
				audit.Log(ctx, "evaluator.step", map[string]interface{}{
					"message": fmt.Sprintf("Compile step: skipped (compile_once_per_eval). %s", compileRes.Summary),
					"step":    StepCompile, "ok": true, "summary": compileRes.Summary, "skipped": true,
				})
			}
		} else {
			compileRes = RunCompile(ctx, runner, opts)
			if compileRes.OK {
				compileSucceededThisEval = true
			}
			if audit != nil {
				audit.Log(ctx, "evaluator.step", map[string]interface{}{
					"message": fmt.Sprintf("Compile step: ok=%v. %s", compileRes.OK, compileRes.Summary),
					"step":    StepCompile, "ok": compileRes.OK, "summary": compileRes.Summary,
				})
			}
		}
		if iter == 0 && !firstIterCompileRecorded {
			firstIterCompileOK = compileRes.OK
			firstIterCompileRecorded = true
		}
		out.StepResults = append(out.StepResults[:0], compileRes)
		if !compileRes.OK {
			unitFailStreak, e2eFailStreak = 0, 0
			unitFailFP, e2eFailFP = "", ""
			out.LastFixAction = FixImportsMocks
			if audit != nil {
				audit.LogError(ctx, "evaluator.compile_failed", map[string]interface{}{
					"message": fmt.Sprintf("Compile failed; suggested action: fix imports/mocks. %s", compileRes.Summary),
					"action":  FixImportsMocks, "output": compileRes.Output,
				})
			}
			if tryAutoFixCSharpMissingProjectReferences(ctx, opts, compileRes.Output, audit) {
				continue
			}
			reportNuGetRestoreFailure(ctx, opts, compileRes.Output, audit)
			// Scoped-compile fallback: when the top-level sln build failed because a sibling project's NuGet
			// restore errored (NU1301/NU1101/NU1102/NU1103/NU1403/NU5036) and the failing sibling is not the
			// artifact's consumer, retry the compile scoped to just the artifact's enclosing project. This
			// excludes the failing sibling from the restore/build graph and lets the evaluation proceed when
			// the tests don't actually depend on whatever the sibling needs authenticated access to.
			if !scopedCompileTried {
				scopedStart := nowFunc()
				if scoped, _, attempted := tryScopedCompileForNuGetFailure(ctx, runner, &opts, compileRes.Output, audit); attempted {
					scopedCompileTried = true
					if scoped.OK {
						compileRes = stampDuration(scoped, scopedStart)
						compileSucceededThisEval = true
						out.StepResults = append(out.StepResults[:0], compileRes)
						if audit != nil {
							audit.Log(ctx, "evaluator.step", map[string]interface{}{
								"message": fmt.Sprintf("Compile step: ok=%v (scoped). %s", compileRes.OK, compileRes.Summary),
								"step":    StepCompile, "ok": compileRes.OK, "summary": compileRes.Summary, "scoped": true,
							})
						}
					}
				}
			}
			// If the scoped-compile fallback above flipped compileRes to OK, fall through to the test phase
			// instead of running the out-of-scope skip / LLM-fix branches (which assume compile still failed).
			if compileRes.OK {
				// fall through
			} else {
				if !compileErrorTouchesArtifactScope(compileRes.Output, opts) {
					if audit != nil {
						audit.Log(ctx, "evaluator.fix_skipped_out_of_scope_compile_error", map[string]interface{}{
							"message": "Compile error does not touch generated artifact scope; skipping LLM test-file fixer to avoid degrading tests.",
						})
					}
					// Unrecoverable-environment early exit. When the compile failure is clearly outside the
					// generated artifact's scope (e.g. private NuGet feed auth issues on a sibling project)
					// AND the scoped-compile retry has already been attempted AND the caller opted in via
					// AbortOnUnrecoverableEnvCompileFailure, break out of the fix loop instead of burning
					// remaining iterations on a condition that deterministic retries cannot change.
					// Generated artifacts remain on disk so the operator can run them locally once the
					// environment is fixed (credentials, offline cache, etc.).
					if opts.AbortOnUnrecoverableEnvCompileFailure && scopedCompileTried && nuGetRestoreFailureDetected(compileRes.Output) {
						if audit != nil {
							audit.LogError(ctx, "evaluator.compile_unrecoverable_environment_failure", map[string]interface{}{
								"message":            "Aborting fix loop: compile failure is outside generated artifact scope, scoped retry already attempted, and NuGet restore failure is the root cause. Generated tests remain on disk for local execution once the environment is fixed.",
								"scoped_retry_tried": scopedCompileTried,
								"iteration":          out.Iterations,
								"max_iterations":     opts.MaxFixIterations,
								"remediation":        nuGetRestoreRemediation(sortedSet(failingNuGetFeedURLs(compileRes.Output))),
							})
						}
						break
					}
					continue
				}
				// NuGet restore failure guard. Even when a CS0234/CS0246 error touches the artifact's
				// scope, it is a *symptom* of `dotnet restore` failing on a private feed (NU1301 / NU1101
				// / NU1102 / NU1103 / NU1403 / NU5036) — not something the generated test file can be
				// repaired into building. Calling the LLM fixer here would only risk degrading the test
				// (e.g. stubbing out a real using statement to silence CS0234) while the underlying
				// authentication / reachability issue remains. Reject the fix up front with
				// `evaluator.fix_rejected_low_value` so operators see the real cause in the audit trail
				// and the generated test file stays intact on disk for local execution once credentials
				// are fixed. Rationale documented in csharp_nuget_diagnose.go::nuGetRestoreRemediation.
				if nuGetRestoreFailureDetected(compileRes.Output) {
					failingFeeds := sortedSet(failingNuGetFeedURLs(compileRes.Output))
					if audit != nil {
						audit.Log(ctx, "evaluator.fix_rejected_low_value", map[string]interface{}{
							"message":           "LLM compile fix rejected: compile failure is a NuGet restore symptom (NU1301/NU1101/NU1102/NU1103/NU1403/NU5036). Code-level edits cannot repair authentication or feed-reachability issues — fix credentials (vcs.azure_devops.token / runner.azure_devops_nuget_feed_endpoints / runner.private_registry_credentials) and re-run.",
							"step":              StepCompile,
							"reason":            "nuget_restore_failure",
							"failing_feed_urls": failingFeeds,
							"remediation":       nuGetRestoreRemediation(failingFeeds),
						})
					}
					if opts.AbortOnUnrecoverableEnvCompileFailure && scopedCompileTried {
						if audit != nil {
							audit.LogError(ctx, "evaluator.compile_unrecoverable_environment_failure", map[string]interface{}{
								"message":            "Aborting fix loop: compile failure is a NuGet restore symptom that touches generated artifact scope, scoped retry already attempted, and code-level edits cannot repair environment issues. Generated tests remain on disk for local execution once the environment is fixed.",
								"scoped_retry_tried": scopedCompileTried,
								"iteration":          out.Iterations,
								"max_iterations":     opts.MaxFixIterations,
								"remediation":        nuGetRestoreRemediation(failingFeeds),
							})
						}
						break
					}
					continue
				}
				if opts.Fixer != nil && compileFixAttempts < maxCompileFix {
					if applied, touched := applyLLMFix(ctx, opts, StepCompile, compileRes.Output, audit, &compileFixAttempts, maxCompileFix, &compileFixState, ""); applied {
						if len(touched) > 0 {
							out.IterationArtifacts = append(out.IterationArtifacts, touched)
						}
						continue
					}
				}
				continue
			}
		}

		// ----- Step 2: Test (unit pass) -----
		unitCmd := resolveUnitTestCommand(opts)
		testRes := RunTest(ctx, runner, opts, unitCmd)
		out.StepResults = append(out.StepResults, testRes)
		if audit != nil {
			audit.Log(ctx, "evaluator.step", map[string]interface{}{
				"message": fmt.Sprintf("Test step (unit): ok=%v. %s", testRes.OK, testRes.Summary),
				"step":    StepTest, "ok": testRes.OK, "summary": testRes.Summary,
				"pass": "unit", "command": unitCmd,
			})
		}
		if !testRes.OK {
			e2eFailStreak, e2eFailFP = 0, ""
			out.LastFixAction = FixAssumptions
			if audit != nil {
				audit.LogError(ctx, "evaluator.test_failed", map[string]interface{}{
					"message": fmt.Sprintf("Unit tests failed; suggested action: adjust assumptions. %s", testRes.Summary),
					"action":  FixAssumptions, "output": testRes.Output, "pass": "unit",
				})
			}
			infraKind := errclass.Kind(opts.Lang, testRes.Output)
			if infraKind != "" && audit != nil {
				audit.Log(ctx, "evaluator.test_failure_classified_infrastructure", map[string]interface{}{
					"message": fmt.Sprintf("Test failure classified as infrastructure/environment (%s).", infraKind),
					"kind":    infraKind,
					"step":    StepTest,
				})
			}
			if infraKind != "" && opts.SkipFixerOnInfrastructureFailure {
				if repThr > 0 && maybeExitOnRepeatedTestFailure(ctx, opts, testRes.Output, &out, audit, &unitFailStreak, &unitFailFP, repThr, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
					return out, nil
				}
				continue
			}
			if opts.Fixer != nil && testFixAttempts < maxTestFix {
				if applied, touched := applyLLMFix(ctx, opts, StepTest, testRes.Output, audit, &testFixAttempts, maxTestFix, &testFixState, infraKind); applied {
					if len(touched) > 0 {
						out.IterationArtifacts = append(out.IterationArtifacts, touched)
					}
					continue
				}
			}
			// RC3: the fixer can no longer make progress on this failing step (the circuit-breaker tripped,
			// or the fix budget is exhausted). Rather than spend the rest of the iteration budget re-running
			// a suite that will keep failing, discard the offending generated artifact(s) so the remaining
			// tests can compile and pass. Decisive for compile-shaped failures, where one un-fixable file
			// fails the whole module yet the early-exit-by-fingerprint path never accumulates.
			if (testFixState.tripped || testFixAttempts >= maxTestFix) &&
				exitByDiscardingStuckArtifacts(ctx, opts, StepTest, testRes.Output, &out, audit, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
				return out, nil
			}
			if repThr > 0 && maybeExitOnRepeatedTestFailure(ctx, opts, testRes.Output, &out, audit, &unitFailStreak, &unitFailFP, repThr, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
				return out, nil
			}
			continue
		}
		unitFailStreak, unitFailFP = 0, ""

		// ----- Step 2b: Test (E2E pass: JS/TS Playwright/Cypress; Java Failsafe/integrationTest; .NET filtered test) -----
		if opts.RunE2ETestPass && dualE2EPassSupportedLang(opts.Lang) {
			e2eCmd := resolveE2ETestCommand(opts)
			if strings.TrimSpace(e2eCmd) != "" {
				testE2E := RunTestE2E(ctx, runner, opts, e2eCmd)
				testE2E.Step = StepTestE2E
				if testE2E.Summary != "" && !strings.HasPrefix(strings.ToLower(testE2E.Summary), "e2e") {
					testE2E.Summary = "e2e: " + testE2E.Summary
				}
				out.StepResults = append(out.StepResults, testE2E)
				if audit != nil {
					audit.Log(ctx, "evaluator.step", map[string]interface{}{
						"message": fmt.Sprintf("Test step (e2e): ok=%v. %s", testE2E.OK, testE2E.Summary),
						"step":    StepTestE2E, "ok": testE2E.OK, "summary": testE2E.Summary,
						"pass": "e2e", "command": e2eCmd,
					})
				}
				if !testE2E.OK {
					out.LastFixAction = FixAssumptions
					if audit != nil {
						audit.LogError(ctx, "evaluator.test_e2e_failed", map[string]interface{}{
							"message": fmt.Sprintf("E2E tests failed; suggested action: adjust assumptions. %s", testE2E.Summary),
							"action":  FixAssumptions, "output": testE2E.Output, "pass": "e2e",
						})
					}
					infraE2E := errclass.Kind(opts.Lang, testE2E.Output)
					if infraE2E != "" && audit != nil {
						audit.Log(ctx, "evaluator.test_failure_classified_infrastructure", map[string]interface{}{
							"message": fmt.Sprintf("E2E failure classified as infrastructure/environment (%s).", infraE2E),
							"kind":    infraE2E,
							"step":    StepTestE2E,
						})
					}
					if infraE2E != "" && opts.SkipFixerOnInfrastructureFailure {
						if repThr > 0 && maybeExitOnRepeatedTestFailure(ctx, opts, testE2E.Output, &out, audit, &e2eFailStreak, &e2eFailFP, repThr, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
							return out, nil
						}
						continue
					}
					if opts.Fixer != nil && testFixAttempts < maxTestFix {
						if applied, touched := applyLLMFix(ctx, opts, StepTestE2E, testE2E.Output, audit, &testFixAttempts, maxTestFix, &testFixState, infraE2E); applied {
							if len(touched) > 0 {
								out.IterationArtifacts = append(out.IterationArtifacts, touched)
							}
							continue
						}
					}
					// RC3: fixer is stuck on the E2E suite — discard the offending artifact(s) (see StepTest).
					if (testFixState.tripped || testFixAttempts >= maxTestFix) &&
						exitByDiscardingStuckArtifacts(ctx, opts, StepTestE2E, testE2E.Output, &out, audit, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
						return out, nil
					}
					if repThr > 0 && maybeExitOnRepeatedTestFailure(ctx, opts, testE2E.Output, &out, audit, &e2eFailStreak, &e2eFailFP, repThr, compileFixAttempts, testFixAttempts, firstIterCompileOK) {
						return out, nil
					}
					continue
				}
				e2eFailStreak, e2eFailFP = 0, ""
			}
		}

		// ----- Step 3: Lint/format -----
		lintRes := RunLint(ctx, runner, opts)
		out.StepResults = append(out.StepResults, lintRes)
		if audit != nil {
			audit.Log(ctx, "evaluator.step", map[string]interface{}{
				"message": fmt.Sprintf("Lint step: ok=%v. %s", lintRes.OK, lintRes.Summary),
				"step":    StepLint, "ok": lintRes.OK, "summary": lintRes.Summary,
			})
		}
		if !lintRes.OK {
			unitFailStreak, e2eFailStreak = 0, 0
			unitFailFP, e2eFailFP = "", ""
			// Lint failure: treat as fix imports/format (same bucket as compile-style fixes)
			out.LastFixAction = FixImportsMocks
			continue
		}

		// ----- Step 4: Coverage delta (unit test command; avoids re-running E2E in coverage) -----
		covRes := RunCoverage(ctx, runner, opts, resolveUnitTestCommand(opts))
		out.StepResults = append(out.StepResults, covRes)
		if audit != nil {
			audit.Log(ctx, "evaluator.step", map[string]interface{}{
				"message": fmt.Sprintf("Coverage step: ok=%v. %s", covRes.OK, covRes.Summary),
				"step":    StepCoverage, "ok": covRes.OK, "summary": covRes.Summary,
			})
		}
		if !covRes.OK {
			unitFailStreak, e2eFailStreak = 0, 0
			unitFailFP, e2eFailFP = "", ""
			// Coverage is best-effort: record failure in StepResults but do not fail the iteration or trigger the fix loop.
			if audit != nil {
				audit.Log(ctx, "evaluator.coverage_non_fatal", map[string]interface{}{
					"message": fmt.Sprintf("Coverage step failed (non-fatal for stability): %s", covRes.Summary),
					"step":    StepCoverage, "summary": covRes.Summary,
				})
			}
		}

		// ----- Step 5 (optional): Mutation tests for critical modules -----
		if opts.RunMutation && len(opts.CriticalModules) > 0 {
			mutRes := RunMutation(ctx, runner, opts)
			out.StepResults = append(out.StepResults, mutRes)
			if audit != nil {
				audit.Log(ctx, "evaluator.step", map[string]interface{}{
					"message": fmt.Sprintf("Mutation step: ok=%v. %s", mutRes.OK, mutRes.Summary),
					"step":    StepMutation, "ok": mutRes.OK, "summary": mutRes.Summary,
				})
			}
			if !mutRes.OK && mutRes.Summary != "skipped" {
				unitFailStreak, e2eFailStreak = 0, 0
				unitFailFP, e2eFailFP = "", ""
				out.LastFixAction = FixAssumptions
				continue
			}
		}

		// All steps passed
		out.Stable = true
		out.LastFixAction = FixNone
		if audit != nil {
			audit.Log(ctx, "evaluator.stable", map[string]interface{}{
				"message":    fmt.Sprintf("Evaluation stable after %d iteration(s).", out.Iterations),
				"iterations": out.Iterations,
			})
		}
		out.CompileOKAfterGenerate = firstIterCompileOK
		out.CompileFixCount = compileFixAttempts
		out.TestFixCount = testFixAttempts
		out.TestOKWithoutFix = out.Stable && compileFixAttempts == 0 && testFixAttempts == 0
		return out, nil
	}

	// Max iterations reached without stability; if last failure was test, suggest stabilize (flaky)
	if out.LastFixAction == FixAssumptions && out.Iterations >= opts.MaxFixIterations {
		out.LastFixAction = FixStabilize
		if audit != nil {
			audit.LogError(ctx, "evaluator.unstable", map[string]interface{}{
				"message": "Max iterations reached; suggested action: stabilize or downgrade to unit test.",
				"action":  FixStabilize,
			})
		}
	}
	out.CompileOKAfterGenerate = firstIterCompileOK
	out.CompileFixCount = compileFixAttempts
	out.TestFixCount = testFixAttempts
	out.TestOKWithoutFix = false
	return out, nil
}

func effectiveRepeatedTestFailureThreshold(opts EvalOptions) int {
	t := opts.RepeatedTestFailureThreshold
	if t == 0 {
		return 5
	}
	if t < 0 {
		return 0
	}
	return t
}

// sortedFailureFingerprint returns a stable key for a set of failing paths (normalized, sorted, joined). Empty if no paths.
func sortedFailureFingerprint(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	norm := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	for _, p := range paths {
		p = normalizePathForFix(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		norm = append(norm, p)
	}
	if len(norm) == 0 {
		return ""
	}
	sort.Strings(norm)
	return strings.Join(norm, "\x1e")
}

// discardableFailingPaths is the intersection of failing paths and generated artifact paths (only discard files we wrote).
func discardableFailingPaths(failingPaths, generatedPaths []string) []string {
	genSet := make(map[string]bool)
	for _, p := range generatedPaths {
		genSet[normalizePathForFix(p)] = true
	}
	var out []string
	seen := make(map[string]bool)
	for _, p := range failingPaths {
		n := normalizePathForFix(p)
		if genSet[n] && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// hasPassingGeneratedArtifact is true when some generated path is not listed as failing (best-effort from parser).
func hasPassingGeneratedArtifact(generated, failing []string) bool {
	fails := make(map[string]bool)
	for _, p := range failing {
		fails[normalizePathForFix(p)] = true
	}
	for _, p := range generated {
		if p == "" {
			continue
		}
		if !fails[normalizePathForFix(p)] {
			return true
		}
	}
	return false
}

// maybeExitOnRepeatedTestFailure updates streak/fingerprint and, when threshold is reached, sets EarlyExit* fields on out and returns true.
func maybeExitOnRepeatedTestFailure(ctx context.Context, opts EvalOptions, testOutput string, out *EvalWorkflowResult, audit Auditor, streak *int, lastFP *string, thr int, compileFixAttempts, testFixAttempts int, firstIterCompileOK bool) bool {
	if thr <= 0 || len(opts.ArtifactPaths) == 0 {
		return false
	}
	failing := ParseFailingTestPaths(testOutputWithoutPassLines(testOutput), opts.ArtifactPaths)
	fp := sortedFailureFingerprint(failing)
	if fp == "" {
		*streak = 0
		*lastFP = ""
		return false
	}
	if fp == *lastFP {
		*streak++
	} else {
		*streak = 1
		*lastFP = fp
	}
	if *streak < thr {
		return false
	}
	discard := discardableFailingPaths(failing, opts.ArtifactPaths)
	if len(discard) == 0 {
		*streak = 0
		*lastFP = ""
		return false
	}
	hasOther := hasPassingGeneratedArtifact(opts.ArtifactPaths, failing)
	out.EarlyExitDiscardPaths = discard
	out.EarlyExitStableAfterDiscard = hasOther
	out.Stable = hasOther
	if hasOther {
		out.LastFixAction = FixNone
	} else {
		out.LastFixAction = FixStabilize
	}
	out.CompileOKAfterGenerate = firstIterCompileOK
	out.CompileFixCount = compileFixAttempts
	out.TestFixCount = testFixAttempts
	out.TestOKWithoutFix = false
	if audit != nil {
		audit.Log(ctx, "evaluator.repeated_test_failure_exit", map[string]interface{}{
			"message":              fmt.Sprintf("Same failing generated test fingerprint reached %d consecutive iteration(s); stopping fix loop early.", thr),
			"stable_after_discard": hasOther, "discard_paths": discard, "iterations": out.Iterations,
		})
	}
	return true
}

// exitByDiscardingStuckArtifacts is the RC3 escape hatch. When the LLM fixer can no longer make
// progress on a failing test/compile step (its circuit-breaker tripped, or the fix budget ran out) we
// stop burning the iteration budget and instead discard only the generated artifact(s) responsible for
// the failure so the rest of the suite can go green. It mirrors maybeExitOnRepeatedTestFailure's
// EarlyExit* contract but fires immediately — no consecutive-fingerprint streak required — because the
// fixer has already declared defeat, and it attributes compile-shaped failures (where one un-fixable
// test file fails the whole module yet the JUnit failing-test parser yields nothing) by intersecting the
// error's cited paths with the generated artifacts. Gated by RepeatedTestFailureThreshold (a value of -1
// disables discard, preserving run-to-budget behaviour). Returns true when it set EarlyExit* and the
// caller should stop the loop.
func exitByDiscardingStuckArtifacts(ctx context.Context, opts EvalOptions, step SandboxStep, output string, out *EvalWorkflowResult, audit Auditor, compileFixAttempts, testFixAttempts int, firstIterCompileOK bool) bool {
	if effectiveRepeatedTestFailureThreshold(opts) <= 0 || len(opts.ArtifactPaths) == 0 {
		return false
	}
	// Attribute the failure. ParseFailingTestPaths handles both JUnit failing-test lines and compile
	// diagnostics (the offending path/basename appears verbatim in the compiler output); union with an
	// explicit compile-cited-path pass so a container-prefixed or basename-only citation still maps.
	failing := ParseFailingTestPaths(testOutputWithoutPassLines(output), opts.ArtifactPaths)
	failing = append(failing, compileCitedArtifactPaths(output, opts.ArtifactPaths, opts.RepoPath)...)
	discard := discardableFailingPaths(failing, opts.ArtifactPaths)
	if len(discard) == 0 {
		return false
	}
	hasOther := hasPassingGeneratedArtifact(opts.ArtifactPaths, failing)
	out.EarlyExitDiscardPaths = discard
	out.EarlyExitStableAfterDiscard = hasOther
	out.Stable = hasOther
	if hasOther {
		out.LastFixAction = FixNone
	} else {
		out.LastFixAction = FixStabilize
	}
	out.CompileOKAfterGenerate = firstIterCompileOK
	out.CompileFixCount = compileFixAttempts
	out.TestFixCount = testFixAttempts
	out.TestOKWithoutFix = false
	if audit != nil {
		audit.Log(ctx, "evaluator.fix_loop_stuck_artifact_discarded", map[string]interface{}{
			"message":              fmt.Sprintf("Fixer can no longer make progress on step %s; discarding %d offending generated artifact(s) so the rest of the suite can compile and pass instead of burning the remaining iteration budget.", step, len(discard)),
			"step":                 step,
			"stable_after_discard": hasOther,
			"discard_paths":        discard,
			"iterations":           out.Iterations,
		})
	}
	return true
}

// compileCitedArtifactPaths returns the generated artifacts whose file path is cited in a compile-shaped
// error log (javac/Maven `path:[line,col]`, csc, tsc, …). It returns nil unless the output looks like a
// compiler diagnostic, so a normal assertion failure never routes through compile-path attribution.
func compileCitedArtifactPaths(output string, artifactPaths []string, repoRoot string) []string {
	if !errout.IsCompileShaped(output) || len(artifactPaths) == 0 || strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	cited := errout.AllCitedRepoPaths(output, filepath.Clean(repoRoot))
	if len(cited) == 0 {
		return nil
	}
	citedSet := make(map[string]bool, len(cited))
	for _, p := range cited {
		citedSet[normalizePathForFix(p)] = true
	}
	var out []string
	for _, a := range artifactPaths {
		if citedSet[normalizePathForFix(a)] {
			out = append(out, a)
		}
	}
	return out
}

// Auditor is the interface for run-scoped audit logging during evaluation.
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}

// SuggestFix returns a short human-readable suggestion based on LastFixAction.
func SuggestFix(action FixAction) string {
	switch action {
	case FixImportsMocks:
		return "Fix imports/mocks and re-run evaluation"
	case FixAssumptions:
		return "Adjust test assumptions and re-run"
	case FixStabilize:
		return "Stabilize test or downgrade to unit test"
	default:
		return ""
	}
}

func isJSEvalLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	default:
		return false
	}
}

// dualE2EPassSupportedLang is true when a second test step may run after the unit pass (see resolveE2ETestCommand).
func dualE2EPassSupportedLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts", "java", "csharp", "cs":
		return true
	default:
		return false
	}
}

// resolveUnitTestCommand returns the shell command for the unit test pass (UnitTestCommand, else
// TestCommand, else empty = runner default). For C# dual-pass runs whose E2E pass uses the default
// `FullyQualifiedName~E2E` heuristic, the returned command is augmented with the mirror
// `FullyQualifiedName!~E2E` filter so E2E-named tests (notably the Playwright .NET bootstrap smoke
// test `AsqsPlaywrightSmokeE2E`) don't execute in the browser-less unit image. See
// `applyCSharpE2EExclusionToUnitCommand` for the exact partition rules.
func resolveUnitTestCommand(opts EvalOptions) string {
	base := strings.TrimSpace(opts.UnitTestCommand)
	if base == "" {
		base = strings.TrimSpace(opts.TestCommand)
	}
	return applyCSharpE2EExclusionToUnitCommand(base, opts)
}

// EffectiveUnitTestCommand returns the shell command used for the unit evaluation pass (for logging and tooling).
func EffectiveUnitTestCommand(unitTestCommand, testCommand string) string {
	return resolveUnitTestCommand(EvalOptions{UnitTestCommand: unitTestCommand, TestCommand: testCommand})
}

// EffectiveE2ETestCommand returns the resolved E2E test command for logging when RepoPath/BuildTool are unknown (JS/TS defaults only).
func EffectiveE2ETestCommand(e2eTestCommand, e2eFramework string) string {
	return resolveE2ETestCommand(EvalOptions{
		E2ETestCommand: e2eTestCommand,
		E2EFramework:   e2eFramework,
		Lang:           "javascript",
	})
}

// EffectiveE2ETestCommandFromOpts resolves the E2E pass command using full eval options (lang, repo, build tool).
func EffectiveE2ETestCommandFromOpts(o EvalOptions) string {
	return resolveE2ETestCommand(o)
}

// stampDuration sets sr.Started/DurationMs from the supplied start time using the package nowFunc
// seam (see workflow.go-level `nowFunc` declaration). Idempotent: never overwrites a non-zero
// Started so callers that already stamped (e.g. tests injecting fake StepResults) keep their
// values. Used by every step invocation in RunEvaluation so session_attempts.duration_ms reflects
// real wall-clock cost (A.6 — tool attempt durations).
func stampDuration(sr StepResult, start time.Time) StepResult {
	if sr.Started.IsZero() {
		sr.Started = start.UTC()
	}
	if sr.DurationMs == 0 {
		sr.DurationMs = nowFunc().Sub(start).Milliseconds()
	}
	return sr
}

func testCommandForFixStep(opts EvalOptions, step SandboxStep) string {
	switch step {
	case StepTestE2E:
		return resolveE2ETestCommand(opts)
	case StepTest:
		u := resolveUnitTestCommand(opts)
		if u != "" {
			return u
		}
		return strings.TrimSpace(opts.TestCommand)
	default:
		return strings.TrimSpace(opts.TestCommand)
	}
}

// isObviousPassSummaryLine is true for Jest/Vitest-style lines that list passing files. Matching basenames
// there wrongly marks passing generated tests as "failing" and causes unstable discard to remove every artifact.
func isObviousPassSummaryLine(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	u := strings.ToUpper(s)
	if strings.HasPrefix(u, "PASS ") {
		return true
	}
	// Vitest default / some reporters
	if strings.HasPrefix(s, "✓ ") || strings.HasPrefix(s, "✔ ") {
		return true
	}
	return false
}

// testOutputWithoutPassLines drops lines that only report passing suites/files. Remaining text is used for
// path and basename matching against artifactPaths.
func testOutputWithoutPassLines(testOutput string) string {
	var b strings.Builder
	for _, line := range strings.Split(testOutput, "\n") {
		if isObviousPassSummaryLine(line) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// lineLooksLikeFailureContext is true for stack traces, FAIL lines, Maven [ERROR], assertion diffs, etc.
// Used so class/file stem matching does not fire on "Running FooTest" / unrelated INFO lines.
func lineLooksLikeFailureContext(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	if strings.HasPrefix(strings.ToUpper(s), "FAIL ") {
		return true
	}
	if strings.Contains(s, "✗ ") || strings.HasPrefix(s, "❯ ") {
		return true
	}
	if strings.Contains(s, "[ERROR]") {
		return true
	}
	if strings.Contains(s, "AssertionError") || strings.Contains(s, "Expected:") || strings.Contains(s, "Received:") {
		return true
	}
	// JUnit / JS stack: at pkg.Class.method(File.java:12) or (Unknown Source)
	if strings.Contains(s, " at ") || strings.Contains(s, "\tat ") {
		if strings.Contains(s, ".java:") || strings.Contains(s, ".kt:") ||
			strings.Contains(s, ".ts:") || strings.Contains(s, ".tsx:") ||
			strings.Contains(s, ".js:") || strings.Contains(s, "Unknown Source") {
			return true
		}
	}
	return false
}

var mavenFailureLine = regexp.MustCompile(`(?i)failures:\s*[1-9]`)

// testOutputForStemMatch joins lines that likely describe failures; stem (class name) matching runs only here.
func testOutputForStemMatch(testOutput string) string {
	var b strings.Builder
	for _, line := range strings.Split(testOutput, "\n") {
		s := strings.TrimSpace(line)
		if lineLooksLikeFailureContext(line) || mavenFailureLine.MatchString(s) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return testOutput
	}
	return out
}

// ParseFailingTestPaths returns which of the given artifact paths appear in the test failure output (best-effort: path or base name in output). Used to decide single-file vs multi-file discard when max iterations reached.
// For Java, JUnit/Maven often output "FooTest.java:45" or "at com.example.FooTest.bar(FooTest.java:12)"; base name and case-insensitive match are used.
// Pass-only lines (e.g. Jest "PASS path/to/file.test.ts") are excluded so passing generated files are not discarded.
func ParseFailingTestPaths(testOutput string, artifactPaths []string) []string {
	if testOutput == "" || len(artifactPaths) == 0 {
		return nil
	}
	// Path / basename: do not search PASS lines (false positives for multi-artifact runs).
	hay := strings.ReplaceAll(testOutputWithoutPassLines(testOutput), "\\", "/")
	hayLower := strings.ToLower(hay)
	// Stem: only in failure-ish lines so "Running OtherTest" INFO does not mark every *Test class failing.
	stemHay := strings.ReplaceAll(testOutputForStemMatch(testOutput), "\\", "/")
	stemHayLower := strings.ToLower(stemHay)

	var out []string
	for _, p := range artifactPaths {
		if p == "" {
			continue
		}
		norm := filepath.ToSlash(p)
		if strings.Contains(hay, norm) {
			out = append(out, p)
			continue
		}
		base := filepath.Base(p)
		if base == "" {
			continue
		}
		if strings.Contains(hay, base) {
			out = append(out, p)
			continue
		}
		if strings.Contains(hayLower, strings.ToLower(base)) {
			out = append(out, p)
			continue
		}
		// Match by file stem (e.g. "ExistingTest" in "at com.example.ExistingTest.badPort") so class-name-only output still matches *Test.java.
		stem := strings.TrimSuffix(base, filepath.Ext(p))
		if len(stem) >= 5 && strings.Contains(stemHayLower, strings.ToLower(stem)) {
			out = append(out, p)
		}
	}
	return out
}

// FirstFailingPath returns the one path from candidates that appears earliest in testOutput (by first occurrence of path or base name). Used when multiple paths match but only one file is the primary failure (e.g. stack trace mentions several files). Returns "" if testOutput is empty or no candidate appears.
func FirstFailingPath(testOutput string, candidates []string) string {
	if testOutput == "" || len(candidates) == 0 {
		return ""
	}
	normOut := strings.ReplaceAll(testOutput, "\\", "/")
	normOutLower := strings.ToLower(normOut)
	firstIdx := -1
	var firstPath string
	for _, p := range candidates {
		if p == "" {
			continue
		}
		norm := filepath.ToSlash(p)
		idx := strings.Index(normOut, norm)
		if idx < 0 {
			base := filepath.Base(p)
			if base != "" {
				idx = strings.Index(normOut, base)
			}
			if idx < 0 && base != "" {
				idx = strings.Index(normOutLower, strings.ToLower(base))
			}
		}
		if idx >= 0 && (firstIdx < 0 || idx < firstIdx) {
			firstIdx = idx
			firstPath = p
		}
	}
	return firstPath
}

// StepSummary returns a one-line summary of step results for logging.
func StepSummary(results []StepResult) string {
	var parts []string
	for _, r := range results {
		status := "ok"
		if !r.OK {
			status = "fail"
		}
		parts = append(parts, string(r.Step)+"="+status)
	}
	return strings.Join(parts, " ")
}

// FailedTestSuiteLabels returns human-readable suite names for failed unit and/or E2E test steps
// in results (order: unit then e2e). Empty if neither step failed.
// Used for scheduler/human-in-the-loop copy: combined unstable means either suite may fail, not both required.
func FailedTestSuiteLabels(results []StepResult) string {
	var labels []string
	for _, r := range results {
		if r.OK {
			continue
		}
		switch r.Step {
		case StepTest:
			labels = append(labels, "unit")
		case StepTestE2E:
			labels = append(labels, "e2e")
		}
	}
	return strings.Join(labels, ", ")
}

// UnstableEligibleForDiscardOrScheduler is true when max-iteration unstable handling (discard paths,
// increment iteration, schedule rerun, human-in-the-loop) should run. Normally this follows
// LastFixAction (assumptions / stabilize after tests, coverage, mutation). We also treat a failing
// unit or E2E step as eligible so rerun scheduling never depends on "both suites failed"—either
// failing suite makes the run unstable (see FailedTestSuiteLabels).
func UnstableEligibleForDiscardOrScheduler(eval EvalWorkflowResult) bool {
	if eval.LastFixAction == FixAssumptions || eval.LastFixAction == FixStabilize {
		return true
	}
	return FailedTestSuiteLabels(eval.StepResults) != ""
}

// manifestPathsForLang returns repo-relative paths of dependency manifest files to include in fix context so the LLM only suggests packages that exist. Empty for unknown lang.
// monoPrefix is an optional normalized repo-relative directory (e.g. apps/api) where the project root lives.
func manifestPathsForLang(repoPath, lang, monoPrefix string) []string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	monoPrefix = strings.Trim(filepath.ToSlash(strings.TrimSpace(monoPrefix)), "/")
	relUnder := func(name string) string {
		if monoPrefix == "" {
			return name
		}
		return monoPrefix + "/" + name
	}
	manifestDir := repoPath
	if monoPrefix != "" {
		manifestDir = filepath.Join(repoPath, filepath.FromSlash(monoPrefix))
	}
	var paths []string
	switch lang {
	case "javascript", "typescript", "js", "ts":
		p := relUnder("package.json")
		if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(p))); err == nil {
			paths = append(paths, p)
		}
	case "java":
		for _, name := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
			p := relUnder(name)
			if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(p))); err == nil {
				paths = append(paths, p)
			}
		}
	case "csharp", "cs":
		// Single .csproj at project root is common; multi-project would need more context
		entries, _ := os.ReadDir(manifestDir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".csproj") {
				if monoPrefix == "" {
					paths = append(paths, e.Name())
				} else {
					paths = append(paths, monoPrefix+"/"+e.Name())
				}
				break
			}
		}
	}
	return paths
}

// manifestPathsForFixer extends manifestPathsForLang with extra paths so the LLM fixer sees the right dependency context.
// For C#, manifestPathsForLang often only finds a root-level .csproj; generated tests may live under a nested test project
// (e.g. mono test tree). We therefore include the nearest .csproj to each artifact path and common MSBuild props files.
func manifestPathsForFixer(repoPath, lang, monoWorkspace string, artifactAndRelatedPaths []string) []string {
	base := manifestPathsForLang(repoPath, lang, monoWorkspace)
	low := strings.ToLower(strings.TrimSpace(lang))
	if low != "csharp" && low != "cs" {
		return base
	}
	repoPath = filepath.Clean(repoPath)
	seen := make(map[string]bool)
	var out []string
	add := func(rel string) {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}
	for _, p := range base {
		add(p)
	}
	for _, ap := range artifactAndRelatedPaths {
		ap = strings.TrimSpace(filepath.ToSlash(ap))
		if ap == "" || !strings.HasSuffix(strings.ToLower(ap), ".cs") {
			continue
		}
		if rel, ok := dotnetproj.NearestCsprojRel(repoPath, ap); ok {
			add(rel)
		}
	}
	mono := strings.Trim(filepath.ToSlash(strings.TrimSpace(monoWorkspace)), "/")
	tryDirs := []string{"."}
	if mono != "" {
		tryDirs = append(tryDirs, mono)
	}
	for _, dir := range tryDirs {
		for _, name := range []string{"Directory.Packages.props", "Directory.Build.props"} {
			var rel string
			if dir == "." {
				rel = name
			} else {
				rel = dir + "/" + name
			}
			full := filepath.Join(repoPath, filepath.FromSlash(rel))
			if _, err := os.Stat(full); err == nil {
				add(rel)
			}
		}
	}
	return out
}

// fixLoopState tracks consecutive applyLLMFix invocations with identical input so the outer fix
// loop can short-circuit when the same artifact_paths + error_output tuple keeps arriving — that's
// the pathological case where the LLM is producing superficially-different fixes that never clear
// the diagnostic (e.g. "protected access" on an external Spring class the LLM doesn't know about).
// One instance is created per step in RunEvaluation; the state is not thread-safe on purpose —
// the fix loop is serial within an evaluation.
// FixLoopState tracks applyLLMFix invocations so callers can short-circuit a stuck loop. Beyond the
// original consecutive-identical detector it also catches two failure modes a moving-target fixer
// exhibits: oscillation (a small set of error states cycled forever, e.g. swapping `PetType.DOG` for
// `PetType.valueOf` and back) and no-progress (each attempt swaps one compile error for a different
// one so the canonical signature is never identical twice, yet the build never gets closer to green).
// Not thread-safe — one instance per gap or serial eval loop.
type FixLoopState struct {
	lastSignature string
	streak        int
	// tripped becomes true once the breaker has fired; subsequent calls are no-ops (the outer loop
	// will already have seen *attemptCounter == maxAttempts and stopped, but this guards nested or
	// reentrant call paths).
	tripped bool
	// seen counts how many times each canonical signature has appeared this loop; recurrences counts
	// signatures that reappeared after the model had moved to a different one (the hallmark of an
	// oscillation that the consecutive-streak counter alone never catches).
	seen        map[string]int
	recurrences int
	// bestMagnitude is the smallest error magnitude (fixLoopErrorMagnitude) seen so far this loop;
	// magnitudeKnown guards the zero-value first observation. noProgressStreak counts consecutive
	// attempts that failed to beat bestMagnitude — i.e. the fixer is busy but not converging.
	bestMagnitude    int
	magnitudeKnown   bool
	noProgressStreak int
}

// FixLoopRepeatStopThreshold is the number of consecutive applyLLMFix calls sharing the same
// (step, sorted(artifact_paths), canonical(error_output)) signature before the fix loop gives up
// for that step. Three is the smallest value that tolerates a "model reshuffled the same bug" blip
// without letting a truly stuck loop burn the full attempt budget. Keep in sync with DOCUMENTATION.md
// ("Automatic context-hygiene escalation" + "Repeat-failure circuit-breaker" subsections).
const FixLoopRepeatStopThreshold = 3

// FixLoopRecurrenceStopThreshold is how many times a previously-seen signature may reappear (after the
// fixer had moved to a different one) before the loop gives up. Two reappearances is enough to confirm
// an oscillation — a 2-state cycle (A→B→A→B) reaches it on the fourth attempt — while still tolerating
// a single "model briefly revisited an old state then moved on" blip.
const FixLoopRecurrenceStopThreshold = 2

// FixLoopNoProgressStopThreshold is how many consecutive fix attempts may fail to reduce the error
// magnitude (fixLoopErrorMagnitude) below its running minimum before the loop gives up. This is the
// backstop for the pure moving-target case where every attempt produces a *different* error (so neither
// the consecutive-streak nor the recurrence detector fires) yet the build never gets closer to green.
const FixLoopNoProgressStopThreshold = 5

// fixLoopDiagnosticLineRe matches lines that carry a compiler/test diagnostic across the languages the
// evaluator drives (javac/Maven, Gradle, dotnet/MSBuild, tsc, Jest/JUnit/Mockito). It is deliberately
// broad: fixLoopErrorMagnitude only needs a monotonic-ish proxy for "how broken is the build", not a
// precise error count.
var fixLoopDiagnosticLineRe = regexp.MustCompile(`(?i)(cannot find symbol|cannot be applied|incompatible types|cannot be resolved|cannot mock|unnecessary stubbing|missingmethodinvocation|noclassdeffound|assertionerror|compilation error|\bCS[0-9]{3,5}\b|error TS[0-9]+|error:|: error|\[ERROR\])`)

// fixLoopErrorMagnitude is a coarse, monotonic-ish proxy for how broken the build is, used by the
// no-progress breaker. It returns the number of distinct error-bearing lines in the canonical error
// text. Calibration across languages is unnecessary — only the trend matters: a genuinely converging
// fixer drives it down (so the no-progress streak resets), whereas a moving-target fixer that swaps one
// error for another keeps it flat. Lines are de-duplicated so a tool that echoes its error epilogue
// twice does not look like extra errors.
func fixLoopErrorMagnitude(canonicalErr string) int {
	if strings.TrimSpace(canonicalErr) == "" {
		return 0
	}
	seen := make(map[string]bool)
	n := 0
	for _, ln := range strings.Split(canonicalErr, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || seen[t] {
			continue
		}
		if fixLoopDiagnosticLineRe.MatchString(t) {
			seen[t] = true
			n++
		}
	}
	return n
}

// fixLoopSignature returns a short, stable fingerprint for the (step, artifact_paths, error_output)
// tuple. The artifact list is sorted so ordering noise doesn't reset the streak; the error body
// must be canonical (errout.CanonicalForFixLoop: Maven sanitize + csharp duplicate-line collapse) so
// Maven's duplicated [ERROR] epilogue / Gradle's help block / vstest duplicate lines do not mask a
// true-repeat pair behind cosmetic variation. sha1 is sufficient — this is a
// per-evaluation in-memory counter, not a security primitive — and the hex prefix keeps audit
// payloads human-readable.
func fixLoopSignature(step SandboxStep, artifactPaths []string, canonicalError string) string {
	paths := append([]string(nil), artifactPaths...)
	for i, p := range paths {
		paths[i] = normalizePathForFix(p)
	}
	sort.Strings(paths)
	h := sha1.New()
	h.Write([]byte(string(step)))
	h.Write([]byte("\x00"))
	h.Write([]byte(strings.Join(paths, "\x00")))
	h.Write([]byte("\x00"))
	h.Write([]byte(canonicalError))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func mergeFixRequestAuditErrorOutput(ctx context.Context, opts EvalOptions, canonicalErr string, errorOutputRaw string, dedupApplied bool, errorOutputSanitized bool) map[string]interface{} {
	h := sha256.Sum256([]byte(canonicalErr))
	shaHex := hex.EncodeToString(h[:])
	display := canonicalErr
	compression := errout.CompressionNone
	if dedupApplied {
		compression = errout.CompressionDeduped
	}
	dt, comp := errout.CompressForAudit("", canonicalErr, auditErrorOutputMaxRunes)
	display = dt
	if comp == errout.CompressionHeadTail {
		compression = errout.CompressionHeadTail
	}
	llmUsed := false
	if !opts.DisableErrorLogLLMSummary && opts.ErrorLogSummarizer != nil && len([]rune(canonicalErr)) >= errorLogLLMSummaryMinRunes {
		if sum, err := opts.ErrorLogSummarizer(ctx, canonicalErr); err == nil && strings.TrimSpace(sum) != "" {
			display = strings.TrimSpace(sum)
			compression = errout.CompressionLLMSummary
			llmUsed = true
		}
	}
	return map[string]interface{}{
		"error_output":                  display,
		"error_output_runes":            len([]rune(display)),
		"error_output_canonical_runes":  len([]rune(canonicalErr)),
		"error_output_runes_raw":        len([]rune(errorOutputRaw)),
		"error_output_sanitized":        errorOutputSanitized,
		"error_output_sha256":           shaHex,
		"error_output_compression":      compression,
		"error_output_deduplicated":     dedupApplied,
		"error_output_llm_summary_used": llmUsed,
	}
}

// applyLLMFix reads artifact files and their dependency files (source under test), plus dependency manifests (package.json, pom.xml, etc.), calls Fixer.Fix with the failed step and error output, writes back fixed content, and increments the step-specific attempt counter. Returns true only when at least one allowed path was written.
// When step is StepTest or StepTestE2E and FailingTestCandidatePaths is set, failure output is parsed and any failing path in that candidate list is included so pre-existing failing tests (e.g. E2E or port/config) get fix context too.
// loopState is the per-step repeat detector: when the same (step, sorted(artifact_paths),
// canonical(error_output)) signature arrives FixLoopRepeatStopThreshold times in a row, the call
// short-circuits, emits evaluator.fix_rejected_low_value with reason="fix_loop_repeat", and bumps
// *attemptCounter to maxAttempts so the outer loop stops calling us for this step.
// applyLLMFix runs the LLM fixer for one failed step and writes accepted file content to disk.
// Returns (applied, touchedPaths): applied is true when at least one file was written;
// touchedPaths is the repo-relative paths actually written this call (after path remap, scope
// gating, low-value rejection). Both values are read by the outer fix loop to (1) decide
// whether to re-run the step and (2) record per-iteration artifact deltas in
// EvalWorkflowResult.IterationArtifacts (A.5 — per-gap iteration tracking).
func applyLLMFix(ctx context.Context, opts EvalOptions, step SandboxStep, errorOutput string, audit Auditor, attemptCounter *int, maxAttempts int, loopState *FixLoopState, infrastructureFailureKind string) (bool, []string) {
	// Sanitize + dedupe (csharp) into canonical error text for fix-loop signatures and fixer input.
	errorOutputRaw := errorOutput
	sanitizedOnly := errout.Sanitize(opts.Lang, errorOutputRaw)
	canonicalErr := errout.CanonicalForFixLoop(opts.Lang, errorOutputRaw)
	errorOutputSanitized := sanitizedOnly != errorOutputRaw
	dedupApplied := canonicalErr != sanitizedOnly
	errorOutput = canonicalErr

	parseHay := errorOutputRaw
	pathsToRead := opts.ArtifactPaths
	if (step == StepTest || step == StepTestE2E) && len(opts.FailingTestCandidatePaths) > 0 && parseHay != "" {
		failing := ParseFailingTestPaths(parseHay, opts.FailingTestCandidatePaths)
		seen := make(map[string]bool)
		for _, p := range pathsToRead {
			seen[normalizePathForFix(p)] = true
		}
		for _, p := range failing {
			if p == "" {
				continue
			}
			key := normalizePathForFix(p)
			if !seen[key] {
				seen[key] = true
				pathsToRead = append(pathsToRead, p)
			}
		}
	}
	if len(pathsToRead) == 0 {
		return false, nil
	}
	// Repeat-failure circuit-breaker. Compute the signature now that errorOutput is sanitised and
	// pathsToRead is finalised (includes any FailingTestCandidatePaths additions for test steps).
	// We check BEFORE reading files / calling the LLM so a truly stuck loop wastes no further work.
	if loopState != nil {
		// Sticky breaker: once tripped, subsequent calls are immediate no-ops regardless of whether
		// the outer loop honoured the counter bump (defensive against reentrancy / tests).
		if loopState.tripped {
			*attemptCounter = maxAttempts
			return false, nil
		}
		sig := fixLoopSignature(step, pathsToRead, errorOutput)
		mag := fixLoopErrorMagnitude(errorOutput)
		if loopState.seen == nil {
			loopState.seen = make(map[string]int)
		}
		// (a) consecutive-identical streak (original behaviour). (b) non-consecutive recurrence: the
		// signature reset (sig != lastSignature) but we have seen this exact sig earlier in the loop —
		// the model is cycling back through error states it already produced (oscillation).
		if sig == loopState.lastSignature {
			loopState.streak++
		} else {
			if loopState.seen[sig] > 0 {
				loopState.recurrences++
			}
			loopState.streak = 1
			loopState.lastSignature = sig
		}
		loopState.seen[sig]++
		// (c) no-progress: track the smallest error magnitude seen and count consecutive attempts that
		// fail to beat it. A converging fixer drives the magnitude down (resetting the streak); a
		// moving-target fixer that swaps one error for another keeps it flat and trips this backstop.
		if !loopState.magnitudeKnown || mag < loopState.bestMagnitude {
			loopState.bestMagnitude = mag
			loopState.magnitudeKnown = true
			loopState.noProgressStreak = 0
		} else {
			loopState.noProgressStreak++
		}
		tripReason := ""
		switch {
		case loopState.streak >= FixLoopRepeatStopThreshold:
			tripReason = "fix_loop_repeat"
		case loopState.recurrences >= FixLoopRecurrenceStopThreshold:
			tripReason = "fix_loop_oscillation"
		case loopState.noProgressStreak >= FixLoopNoProgressStopThreshold:
			tripReason = "fix_loop_no_progress"
		}
		if tripReason != "" {
			loopState.tripped = true
			if audit != nil {
				sortedPaths := append([]string(nil), pathsToRead...)
				for i, p := range sortedPaths {
					sortedPaths[i] = normalizePathForFix(p)
				}
				sort.Strings(sortedPaths)
				var msg string
				switch tripReason {
				case "fix_loop_oscillation":
					msg = fmt.Sprintf("Fix loop oscillating: previously-seen error signatures reappeared %d time(s) for step %s (the fixer is cycling through the same error states). Skipping further fix attempts so the remaining %d of %d attempts are not burned.", loopState.recurrences, step, maxAttempts-*attemptCounter, maxAttempts)
				case "fix_loop_no_progress":
					msg = fmt.Sprintf("Fix loop not converging: error magnitude failed to improve for %d consecutive attempt(s) on step %s (best=%d, current=%d) — each fix swaps one error for another. Skipping further fix attempts so the remaining %d of %d attempts are not burned.", loopState.noProgressStreak, step, loopState.bestMagnitude, mag, maxAttempts-*attemptCounter, maxAttempts)
				default:
					msg = fmt.Sprintf("Fix loop saturated: the same (step, artifact_paths, error_output) signature arrived %d times in a row for step %s. Skipping further fix attempts for this step so the remaining %d of %d attempts are not burned on the same prompt.", loopState.streak, step, maxAttempts-*attemptCounter, maxAttempts)
				}
				audit.Log(ctx, "evaluator.fix_rejected_low_value", map[string]interface{}{
					"message":                msg,
					"step":                   step,
					"reason":                 tripReason,
					"fix_attempt":            *attemptCounter + 1,
					"max_fix_attempt":        maxAttempts,
					"streak":                 loopState.streak,
					"recurrences":            loopState.recurrences,
					"no_progress_streak":     loopState.noProgressStreak,
					"error_magnitude":        mag,
					"best_error_magnitude":   loopState.bestMagnitude,
					"threshold":              FixLoopRepeatStopThreshold,
					"signature":              sig,
					"artifact_paths":         sortedPaths,
					"error_output_sanitized": errorOutputSanitized,
				})
			}
			// Consume the remaining attempt budget so the outer loop's `*FixAttempts < max*Fix`
			// guard trips and no further applyLLMFix call is issued for this step. Returning
			// false without bumping the counter would leave the loop free to keep calling us.
			*attemptCounter = maxAttempts
			return false, nil
		}
	}
	artifactKeySet := make(map[string]bool)
	for _, p := range opts.ArtifactPaths {
		artifactKeySet[normalizePathForFix(p)] = true
	}
	files := make(map[string]string)
	var missingRequired []string
	var skippedBestEffort []string

	readOne := func(rel string, mustRead bool) {
		norm, ok := ResolveReadableRepoFile(opts.RepoPath, rel, opts.MonoRepoWorkspace)
		if _, dup := files[norm]; dup {
			return
		}
		if !ok {
			if mustRead {
				missingRequired = append(missingRequired, norm)
			} else {
				skippedBestEffort = append(skippedBestEffort, norm)
			}
			return
		}
		full := filepath.Join(opts.RepoPath, filepath.FromSlash(norm))
		body, err := os.ReadFile(full)
		if err != nil {
			if mustRead {
				missingRequired = append(missingRequired, norm)
			} else {
				skippedBestEffort = append(skippedBestEffort, norm)
			}
			return
		}
		files[norm] = string(body)
	}

	for _, rel := range pathsToRead {
		readOne(rel, artifactKeySet[normalizePathForFix(rel)])
	}
	// Include dependency/source files (e.g. controller, repository, service) so the LLM can fix mocks to match real APIs.
	if opts.ArtifactDependencies != nil {
		for _, rel := range opts.ArtifactPaths {
			for _, dep := range opts.ArtifactDependencies[rel] {
				readOne(dep, false)
			}
		}
	}
	// For failing test paths not in ArtifactPaths (pre-existing tests), include their source file so the fixer has context.
	generatedSet := make(map[string]bool)
	for _, p := range opts.ArtifactPaths {
		generatedSet[normalizePathForFix(p)] = true
	}
	for _, rel := range pathsToRead {
		if generatedSet[normalizePathForFix(rel)] {
			continue
		}
		src := retrieval.TestPathToSourcePath(rel, opts.Lang, opts.TestFramework, opts.RepoPath)
		if src != "" {
			src = strings.TrimPrefix(filepath.ToSlash(src), "/")
			readOne(src, false)
		}
	}
	// Paths cited in the error log (stack traces, javac `location: ... of type <FQCN>` hints)
	// may point to sources not yet loaded; pull in a bounded set for fixer context. Phase 2 hygiene
	// change (see docs/DOCUMENTATION.md): we now use errout.AllCitedRepoPaths, which (a) handles
	// Maven's bracket `path:[L,C]` form, (b) resolves Java FQCN symbol/location hints when the
	// file exists, (c) preserves first-appearance order. To keep the prompt bounded we apply both a
	// **count** cap (maxFixerErrorCitedPaths) and a **runes** budget (maxFixerDependencyContextRunes)
	// — the budget is measured against the already-loaded `files` map so artifacts and their
	// ArtifactDependencies always win the prefix; the error-cited tail is trimmed when it would
	// push the total over the budget.
	alreadyPaths := make(map[string]bool)
	loadedRunes := 0
	for k, c := range files {
		alreadyPaths[normalizePathForFix(k)] = true
		loadedRunes += len([]rune(c))
	}
	addedExtras := 0
	for _, rel := range errout.AllCitedRepoPaths(errorOutput, filepath.Clean(opts.RepoPath)) {
		if addedExtras >= maxFixerErrorCitedPaths {
			break
		}
		if loadedRunes >= maxFixerDependencyContextRunes {
			break
		}
		key := normalizePathForFix(rel)
		if alreadyPaths[key] {
			continue
		}
		before := len(files)
		readOne(rel, false)
		if len(files) == before {
			continue
		}
		alreadyPaths[key] = true
		addedExtras++
		if body, ok := files[key]; ok {
			loadedRunes += len([]rune(body))
		}
	}
	// RC1: resolve type names the compiler could not find (e.g. `symbol: class PetType`, `constructor
	// Pet in class …`) to their real repo sources — by package when the FQCN is correct, by basename
	// when the cited package is wrong or missing. Without this the fixer keeps guessing the package /
	// constructor / nature of a type it has never been shown, because no ArtifactDependency declared it
	// and the hallucinated import does not resolve. symbolKeep force-keeps these below so signature
	// slicing and attempt-threshold read-scope narrowing cannot strip the one source the fix needs.
	symbolKeep := make(map[string]bool)
	if strings.TrimSpace(errorOutputRaw) != "" {
		var loadedSymbolPaths []string
		for _, rel := range errout.ResolveMissingTypeFiles(errorOutputRaw, filepath.Clean(opts.RepoPath), opts.Lang, maxMissingTypeFilesPerFix) {
			key := normalizePathForFix(rel)
			if alreadyPaths[key] {
				symbolKeep[key] = true // already in context — still protect it from narrowing
				continue
			}
			if loadedRunes >= maxFixerDependencyContextRunes {
				break
			}
			before := len(files)
			readOne(rel, false)
			if len(files) == before {
				continue
			}
			alreadyPaths[key] = true
			symbolKeep[key] = true
			loadedSymbolPaths = append(loadedSymbolPaths, key)
			if body, ok := files[key]; ok {
				loadedRunes += len([]rune(body))
			}
		}
		if len(loadedSymbolPaths) > 0 && audit != nil {
			sort.Strings(loadedSymbolPaths)
			audit.Log(ctx, "evaluator.fix_missing_type_context_loaded", map[string]interface{}{
				"message": fmt.Sprintf("Loaded %d source file(s) for type name(s) the compiler could not resolve, so the fixer sees their real package/constructor/API instead of guessing.", len(loadedSymbolPaths)),
				"paths":   loadedSymbolPaths,
				"step":    step,
			})
		}
	}
	if len(skippedBestEffort) > 0 && audit != nil {
		sort.Strings(skippedBestEffort)
		audit.Log(ctx, "evaluator.fix_context_paths_unavailable", map[string]interface{}{
			"message": fmt.Sprintf("%d best-effort context path(s) were not readable (missing from disk or mono workspace mismatch); fix continues with remaining files.", len(skippedBestEffort)),
			"paths":   skippedBestEffort,
			"reason":  "not_found",
			"step":    step,
		})
	}
	if len(missingRequired) > 0 && audit != nil {
		sort.Strings(missingRequired)
		audit.LogError(ctx, "evaluator.fix_missing_required_context", map[string]interface{}{
			"message": fmt.Sprintf("%d generated artifact path(s) could not be read for LLM fix.", len(missingRequired)),
			"paths":   missingRequired,
			"step":    step,
		})
	}
	// Include dependency manifests so the LLM only suggests imports/packages that exist (e.g. package.json, pom.xml).
	manifests := make(map[string]string)
	for _, rel := range manifestPathsForFixer(opts.RepoPath, opts.Lang, opts.MonoRepoWorkspace, pathsToRead) {
		rel = filepath.FromSlash(rel)
		full := filepath.Join(opts.RepoPath, rel)
		body, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		manifests[rel] = string(body)
	}
	if len(files) == 0 {
		return false, nil
	}
	// Compute the current fix attempt before the context-hygiene branches so automatic
	// escalation can flip opt-in flags on repeat failures. The canonical `attempt` variable
	// assigned to the FixRequest is still computed once, just above the request struct below.
	currentAttempt := *attemptCounter + 1
	autoEscalate := currentAttempt >= FixAttemptAutoEscalationThreshold
	effectiveSignatureOnly := opts.FixerDependencySignatureOnly || autoEscalate
	signatureOnlyAutoForced := effectiveSignatureOnly && !opts.FixerDependencySignatureOnly
	if autoEscalate && audit != nil {
		audit.Log(ctx, "evaluator.fix_auto_escalated", map[string]interface{}{
			"message":                          fmt.Sprintf("Fix attempt %d of %d crossed the auto-escalation threshold (%d): forcing dependency_signature_only and structured_user_message on regardless of YAML defaults so the LLM sees a different context shape on this and later attempts.", currentAttempt, maxAttempts, FixAttemptAutoEscalationThreshold),
			"step":                             step,
			"fix_attempt":                      currentAttempt,
			"max_fix_attempt":                  maxAttempts,
			"threshold":                        FixAttemptAutoEscalationThreshold,
			"dependency_signature_only_forced": signatureOnlyAutoForced,
			"dependency_signature_only_config": opts.FixerDependencySignatureOnly,
			"structured_user_message_forced":   !opts.FixerStructuredUserMessage,
			"structured_user_message_config":   opts.FixerStructuredUserMessage,
			"reason":                           "fix_attempt_threshold",
		})
	}
	// Phase-3 dependency slicing (opt-in via FixerDependencySignatureOnly, or auto-forced once
	// currentAttempt crosses FixAttemptAutoEscalationThreshold — see evaluator.fix_auto_escalated
	// audit event). The slice-safe set is the union of: all artifact paths, all declared
	// ArtifactDependencies[artifact] entries, and everything errout.AllCitedRepoPaths named.
	// Any file outside that set keeps only its class/interface headers, field declarations, and
	// method signatures so the prompt stays focused on code the LLM is actually allowed to call
	// or edit. If slicing fails (syntax we don't recognise, mismatched braces)
	// fixslice.SliceSignatures returns the input unchanged, so the worst case is "nothing shrank".
	var slicedPaths []string
	if effectiveSignatureOnly {
		keep := make(map[string]bool, len(files))
		for _, p := range opts.ArtifactPaths {
			keep[normalizePathForFix(p)] = true
		}
		if opts.ArtifactDependencies != nil {
			for _, deps := range opts.ArtifactDependencies {
				for _, d := range deps {
					keep[normalizePathForFix(d)] = true
				}
			}
		}
		for _, p := range errout.AllCitedRepoPaths(errorOutput, filepath.Clean(opts.RepoPath)) {
			keep[normalizePathForFix(p)] = true
		}
		// RC1: never slice away the bodies of sources we loaded specifically because the compiler could
		// not resolve their type — the fixer needs the real constructor/fields/whether it is an enum.
		for k := range symbolKeep {
			keep[k] = true
		}
		slicedRunesSaved := 0
		for path, body := range files {
			if keep[path] {
				continue
			}
			sliced := fixslice.SliceSignatures(opts.Lang, path, body)
			if sliced == body {
				continue
			}
			slicedRunesSaved += len([]rune(body)) - len([]rune(sliced))
			files[path] = sliced
			slicedPaths = append(slicedPaths, path)
		}
		if len(slicedPaths) > 0 {
			sort.Strings(slicedPaths)
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_dependency_sliced", map[string]interface{}{
					"message":            fmt.Sprintf("Sliced %d read-only dependency file(s) to signatures (saved %d runes).", len(slicedPaths), slicedRunesSaved),
					"step":               step,
					"sliced_paths":       slicedPaths,
					"sliced_runes_saved": slicedRunesSaved,
				})
			}
		}
	}
	// Collect the full set of retrieval/generation contexts the caller supplied before any
	// scope narrowing. We rebuild a writable-only view below so the LLM prompt doesn't carry
	// <artifact_context> blocks for files that have just been dropped from the writable-allow
	// list (or the read-scope allow list), which would otherwise reintroduce 100K+ runes of
	// out-of-scope context and defeat the whole point of narrowing.
	fullArtifactCtx := make(map[string]string)
	for _, rel := range pathsToRead {
		key := normalizePathForFix(rel)
		ctxStr := strings.TrimSpace(opts.ArtifactContexts[key])
		if ctxStr == "" {
			ctxStr = strings.TrimSpace(opts.ArtifactContexts[rel])
		}
		if ctxStr == "" {
			continue
		}
		fullArtifactCtx[key] = ctxStr
	}
	// writableArtifacts is the set the fixer may WRITE. It starts equal to the full artifact set
	// and is narrowed for StepTest/StepTestE2E failures whose error output explicitly names a
	// subset of artifacts — the artifacts not named stay in Files (read-only context) so nothing is
	// hidden from the LLM, but the write-allow-list (basename remap + fixOutputPathAllowed) is
	// tightened so a fix targeted at Test A can't silently rewrite Test B. The guard only kicks in
	// when the intersection is non-empty and strictly smaller than the full set; otherwise we
	// would either let writes through with no guidance (empty intersection) or narrow to a no-op.
	// Defensive dedupe: opts.ArtifactPaths can arrive with duplicate entries when upstream
	// orchestration queued the same target twice (e.g. a fresh generate + ExtendExisting update
	// for the same IT file). Downstream audit (artifact_paths), the write-allow-list, and the
	// structured user message are all sets, so emit each path at most once. First-seen order
	// is preserved to keep audit diffs stable across releases. The orchestrator already dedupes
	// at assembly time, but fixers called directly by tests or future callers should not depend
	// on that invariant holding upstream.
	writableArtifacts := make([]string, 0, len(pathsToRead))
	{
		seen := make(map[string]bool, len(pathsToRead))
		for _, p := range pathsToRead {
			key := normalizePathForFix(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			writableArtifacts = append(writableArtifacts, p)
		}
	}
	scopeNarrowed := false
	var scopedAuditPaths []string
	if (step == StepTest || step == StepTestE2E) && len(opts.ArtifactPaths) > 0 && strings.TrimSpace(errorOutput) != "" {
		cited := errout.AllCitedRepoPaths(errorOutput, filepath.Clean(opts.RepoPath))
		if len(cited) > 0 {
			citedSet := make(map[string]bool, len(cited))
			for _, p := range cited {
				citedSet[normalizePathForFix(p)] = true
			}
			// Scoped set must also dedupe: if opts.ArtifactPaths arrived with dups AND the
			// error cited the same path, we'd otherwise carry the dup through.
			scoped := make([]string, 0, len(opts.ArtifactPaths))
			scopedSeen := make(map[string]bool, len(opts.ArtifactPaths))
			uniqueArtifactCount := 0
			seenArtifact := make(map[string]bool, len(opts.ArtifactPaths))
			for _, a := range opts.ArtifactPaths {
				key := normalizePathForFix(a)
				if !seenArtifact[key] {
					seenArtifact[key] = true
					uniqueArtifactCount++
				}
				if !citedSet[key] || scopedSeen[key] {
					continue
				}
				scopedSeen[key] = true
				scoped = append(scoped, a)
			}
			if len(scoped) > 0 && len(scoped) < uniqueArtifactCount {
				writableArtifacts = scoped
				scopeNarrowed = true
				scopedAuditPaths = append([]string(nil), scoped...)
			}
		}
	}
	// Narrow the artifact-context map to the post-scope writable allow-list. When scopeNarrowed
	// is false writableArtifacts == pathsToRead so this is a structural no-op (artifactCtx ==
	// fullArtifactCtx). When scopeNarrowed is true we drop every entry keyed by a non-writable
	// artifact; these would otherwise land in the prompt as <artifact_context> blocks for test
	// files the LLM has been told it cannot modify.
	writableKeys := make(map[string]bool, len(writableArtifacts))
	for _, p := range writableArtifacts {
		writableKeys[normalizePathForFix(p)] = true
	}
	artifactCtx := make(map[string]string, len(fullArtifactCtx))
	var droppedArtifactCtxPaths []string
	droppedArtifactCtxRunes := 0
	for key, v := range fullArtifactCtx {
		if writableKeys[key] {
			artifactCtx[key] = v
			continue
		}
		droppedArtifactCtxPaths = append(droppedArtifactCtxPaths, key)
		droppedArtifactCtxRunes += len([]rune(v))
	}
	sort.Strings(droppedArtifactCtxPaths)
	if scopeNarrowed && audit != nil {
		audit.Log(ctx, "evaluator.fix_scope_narrowed", map[string]interface{}{
			"message":                        fmt.Sprintf("Narrowed writable-artifact scope for step %s from %d to %d path(s) based on error output (dropped %d artifact-context entr(ies), %d runes).", step, len(opts.ArtifactPaths), len(scopedAuditPaths), len(droppedArtifactCtxPaths), droppedArtifactCtxRunes),
			"step":                           step,
			"artifact_paths_all":             append([]string(nil), opts.ArtifactPaths...),
			"artifact_paths_scoped":          scopedAuditPaths,
			"reason":                         "error_cited_subset",
			"error_output_sanitized":         errorOutputSanitized,
			"artifact_context_dropped_paths": droppedArtifactCtxPaths,
			"artifact_context_dropped_runes": droppedArtifactCtxRunes,
		})
	}

	// Attempt-driven READ-scope narrowing: mirror the write-side narrowing on the read side once
	// currentAttempt crosses FixAttemptAutoEscalationThreshold. The signature slicer above has
	// already elided bodies for every non-artifact / non-declared-dep / non-error-cited file,
	// but at attempt 3+ even signatures are too much context for the failure mode we're trying
	// to escape (the LLM ignoring a 192K-rune prompt). Drop from Files everything that isn't
	// (a) the writable artifact itself, (b) a declared ArtifactDependencies entry for one of the
	// writable artifacts — these are the "direct imports" the plan calls out — or (c) the
	// reverse-mapped source of a writable artifact (TestPathToSourcePath). Manifests live in a
	// separate map and are untouched. Error-cited paths that don't meet any of the above are
	// intentionally dropped on this tier: at this attempt number the signal the LLM is missing
	// is rarely in a random stack-trace neighbour. The drop is logged so operators can spot when
	// narrowing is actively in effect.
	if autoEscalate && len(files) > 0 {
		keep := make(map[string]bool, len(writableArtifacts)*4)
		for _, p := range writableArtifacts {
			keep[normalizePathForFix(p)] = true
		}
		for _, art := range writableArtifacts {
			for _, dep := range opts.ArtifactDependencies[art] {
				keep[normalizePathForFix(dep)] = true
			}
			if src := retrieval.TestPathToSourcePath(art, opts.Lang, opts.TestFramework, opts.RepoPath); src != "" {
				keep[normalizePathForFix(src)] = true
			}
		}
		// RC1: keep sources loaded for compiler-unresolved type names (e.g. PetType when the test used a
		// wrong import). These are exactly the files the fixer is missing, so read-scope narrowing — the
		// very thing that previously starved the loop — must not drop them.
		for k := range symbolKeep {
			keep[k] = true
		}
		var dropped []string
		droppedRunes := 0
		for path, body := range files {
			if keep[path] {
				continue
			}
			dropped = append(dropped, path)
			droppedRunes += len([]rune(body))
			delete(files, path)
		}
		if len(dropped) > 0 && audit != nil {
			sort.Strings(dropped)
			keptPaths := make([]string, 0, len(files))
			for p := range files {
				keptPaths = append(keptPaths, p)
			}
			sort.Strings(keptPaths)
			audit.Log(ctx, "evaluator.fix_read_scope_narrowed", map[string]interface{}{
				"message":       fmt.Sprintf("Attempt %d crossed the escalation threshold (%d): dropped %d dependency file(s) (%d runes) outside the writable-artifact allowlist (artifact + declared deps + reverse-mapped source).", currentAttempt, FixAttemptAutoEscalationThreshold, len(dropped), droppedRunes),
				"step":          step,
				"fix_attempt":   currentAttempt,
				"threshold":     FixAttemptAutoEscalationThreshold,
				"dropped_paths": dropped,
				"dropped_runes": droppedRunes,
				"kept_paths":    keptPaths,
				"reason":        "fix_attempt_threshold",
			})
		}
	}

	attempt := currentAttempt
	req := FixRequest{
		Step:                      step,
		ErrorOutput:               errorOutput,
		Files:                     files,
		ArtifactPaths:             writableArtifacts,
		ArtifactContexts:          artifactCtx,
		RepoPath:                  opts.RepoPath,
		Lang:                      opts.Lang,
		TestFramework:             opts.TestFramework,
		BuildTool:                 opts.BuildTool,
		CompileCommand:            opts.CompileCommand,
		TestCommand:               testCommandForFixStep(opts, step),
		Manifests:                 manifests,
		FixAttempt:                attempt,
		MaxFixAttempt:             maxAttempts,
		InfrastructureFailureKind: strings.TrimSpace(infrastructureFailureKind),
		GapSessionID:              strings.TrimSpace(opts.GapSessionID),
	}
	filePaths := make([]string, 0, len(files))
	for p := range files {
		filePaths = append(filePaths, p)
	}
	sort.Strings(filePaths)
	// Split the path surface so audit readers can tell at a glance which files are writable
	// targets, which are read-only dependencies the LLM may reference, and which manifests it must
	// respect for imports. manifestPaths has its own sub-set because manifests live in req.Manifests
	// (separate from req.Files) so they never appear in filePaths at all.
	artifactPathSet := make(map[string]bool, len(writableArtifacts))
	for _, p := range writableArtifacts {
		artifactPathSet[normalizePathForFix(p)] = true
	}
	artifactPathsAudit := make([]string, 0, len(writableArtifacts))
	for _, p := range writableArtifacts {
		artifactPathsAudit = append(artifactPathsAudit, normalizePathForFix(p))
	}
	sort.Strings(artifactPathsAudit)
	dependencyPaths := make([]string, 0, len(filePaths))
	for _, p := range filePaths {
		if artifactPathSet[p] {
			continue
		}
		dependencyPaths = append(dependencyPaths, p)
	}
	manifestPathsAudit := sortedMapKeys(manifests)
	contextDump := formatFixRequestDump(step, errorOutput, files)
	if audit != nil {
		artifactContextCount := len(artifactCtx)
		artifactContextRunes := 0
		for _, c := range artifactCtx {
			artifactContextRunes += len([]rune(c))
		}
		payload := map[string]interface{}{
			"message":                         fmt.Sprintf("LLM fix requested for step %s (%d files).", step, len(files)),
			"step":                            step,
			"artifact_paths":                  artifactPathsAudit,
			"dependency_paths":                dependencyPaths,
			"manifest_paths":                  manifestPathsAudit,
			"context_dump":                    contextDump,
			"context_dump_length":             len(contextDump),
			"artifact_context_count":          artifactContextCount,
			"artifact_context_total_runes":    artifactContextRunes,
			"artifact_context_artifact_paths": sortedMapKeys(artifactCtx),
			"fix_attempt":                     attempt,
			"max_fix_attempt":                 maxAttempts,
			"scope_narrowed":                  scopeNarrowed,
			"dependency_sliced_paths":         slicedPaths,
			"dependency_signature_only":       effectiveSignatureOnly,
			"auto_escalated":                  autoEscalate,
		}
		for k, v := range mergeFixRequestAuditErrorOutput(ctx, opts, errorOutput, errorOutputRaw, dedupApplied, errorOutputSanitized) {
			payload[k] = v
		}
		// Pre-set structured_user_message to the auto-escalated resolution so it wins over the
		// introspector's f.StructuredUserMessage readout (the introspector loop below skips any
		// key already present in payload). The fixer itself mirrors this escalation by consulting
		// req.FixAttempt >= FixAttemptAutoEscalationThreshold in its message builder.
		if opts.FixerStructuredUserMessage || autoEscalate {
			payload["structured_user_message"] = true
		}
		// Per-turn multi_turn accuracy: the introspector returns f.MultiTurnRepair (static config),
		// but on auto-escalated turns where StructuredUserMessage was NOT already set in config the
		// fixer drops prior conversation messages so the LLM sees a fresh prompt (escalatedThisTurn
		// in llmfix.go). In that case the audit payload should report multi_turn=false regardless
		// of the static flag, otherwise operators reading fix_request logs conclude conversation
		// history was being re-sent when it wasn't. When StructuredUserMessage was already on via
		// config, escalation does NOT bypass conv history, so we leave the introspector's value.
		if autoEscalate && !opts.FixerStructuredUserMessage {
			payload["multi_turn"] = false
		}
		// One-release compatibility shim: existing consumers still look for file_paths as the
		// union of all files shipped to the fixer (artifacts + dependencies, excluding manifests
		// which live in a different map). DOCUMENTATION.md announces the deprecation — drop this
		// line once downstream tooling has switched to the split keys.
		payload["file_paths"] = filePaths
		if intro, ok := opts.Fixer.(FixRequestIntrospector); ok {
			for k, v := range intro.FixRequestAuditMetadata() {
				if _, exists := payload[k]; exists {
					continue
				}
				payload[k] = v
			}
		}
		audit.Log(ctx, "evaluator.fix_request", payload)
	}
	resp, err := opts.Fixer.Fix(ctx, req)
	if err != nil {
		if audit != nil {
			audit.LogError(ctx, "evaluator.fix_llm_error", map[string]interface{}{
				"message": fmt.Sprintf("LLM fix failed: %s", err.Error()),
				"error":   err.Error(), "context_dump": contextDump, "context_dump_length": len(contextDump),
			})
		}
		return false, nil
	}
	if len(resp.Files) == 0 {
		if audit != nil {
			audit.Log(ctx, "evaluator.fix_empty_response", map[string]interface{}{
				"message": "LLM fix returned no files to apply; response may be invalid JSON or empty object.",
				"step":    step,
			})
		}
		return false, nil
	}
	// Basename remap targets **generated artifacts only** (not dependency/source paths in pathsToRead),
	// so e.g. lifecycles.ts from the LLM cannot steal writes from lifecycles.test.ts.
	// When write-scope has been narrowed by the error output we restrict the remap source set the
	// same way so basename collisions cannot leak a write back to an out-of-scope artifact.
	remapBasis := opts.ArtifactPaths
	if scopeNarrowed {
		remapBasis = writableArtifacts
	}
	artifactBaseToPaths := make(map[string][]string)
	for _, p := range remapBasis {
		norm := normalizePathForFix(p)
		base := filepath.Base(norm)
		artifactBaseToPaths[base] = append(artifactBaseToPaths[base], norm)
	}
	pathsUpdated := make([]string, 0, len(resp.Files))
	for rel, content := range resp.Files {
		relClean := normalizePathForFix(rel)
		// If LLM returned a path not in pathsToRead, try to match by base name to a **generated** artifact only.
		if len(opts.ArtifactPaths) > 0 {
			found := false
			for _, a := range pathsToRead {
				if normalizePathForFix(a) == relClean {
					found = true
					break
				}
			}
			if !found {
				if matches := artifactBaseToPaths[filepath.Base(relClean)]; len(matches) > 0 {
					chosen := chooseArtifactAmongBasenameMatches(matches, errorOutput)
					relClean = normalizePathForFix(chosen)
				}
			}
		}
		relForWrite := filepath.FromSlash(relClean)
		// Use narrowed write-allow list when scopeNarrowed so the writable set matches what we
		// communicated to the LLM via FixRequest.ArtifactPaths.
		writeOpts := opts
		if scopeNarrowed {
			writeOpts.ArtifactPaths = writableArtifacts
		}
		if !fixOutputPathAllowed(relClean, writeOpts, pathsToRead) {
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_skip_path", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix skipped write to non-test or non-artifact path %s (implementation files are read-only in fix context).", relClean),
					"path":    relClean,
					"step":    step,
				})
			}
			continue
		}
		if strings.TrimSpace(content) == "" {
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_skip_empty", map[string]interface{}{
					"message": "LLM fix skipped empty content (would erase test file); keeping existing file on disk.",
					"path":    relClean,
					"step":    step,
				})
			}
			continue
		}
		// Absolute gate: never accept a test file that has no test methods — even if the previous on-disk body
		// was also empty (so introducedLowValueFixReason would silently pass it through). An empty shell like
		// `package x; class FooIT {}` compiles but runs zero assertions, and accepting it effectively ends the
		// fix loop on a success that adds no coverage.
		if reason := EmptyTestFileReason(relClean, content); reason != "" {
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_rejected_low_value", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix rejected for %s: %s.", relClean, reason),
					"path":    relClean,
					"step":    step,
					"reason":  reason,
				})
			}
			continue
		}
		// Absolute structural gate: reject files with obvious syntactic garbage (markdown fences
		// leaked into the source, unbalanced braces from truncation, no top-level class/interface
		// /enum/record for Java-C#, missing package decl for Go) BEFORE we write them to disk.
		// Otherwise javac/roslyn/go-build rejects the file a full sandbox iteration later with a
		// "class, interface, enum, or record expected" style error at line 2 — that wastes an
		// entire LLM turn and muddies the audit trail. Same event name as the empty-test gate
		// (evaluator.fix_rejected_low_value) so downstream dashboards see a single stream of
		// quality-gate rejections.
		if reason := SyntacticShellReason(relClean, content); reason != "" {
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_rejected_low_value", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix rejected for %s: %s.", relClean, reason),
					"path":    relClean,
					"step":    step,
					"reason":  reason,
				})
			}
			continue
		}
		if reason := introducedLowValueFixReason(relClean, files[relClean], content); reason != "" {
			if audit != nil {
				audit.Log(ctx, "evaluator.fix_rejected_low_value", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix rejected for %s: would degrade test quality (%s).", relClean, reason),
					"path":    relClean,
					"step":    step,
					"reason":  reason,
				})
			}
			continue
		}
		full := filepath.Join(opts.RepoPath, relForWrite)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			if audit != nil {
				audit.LogError(ctx, "evaluator.fix_mkdir", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix: cannot create dir for %s: %v", relClean, err),
					"path":    full, "error": err.Error(),
				})
			}
			continue
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			if audit != nil {
				audit.LogError(ctx, "evaluator.fix_write", map[string]interface{}{
					"message": fmt.Sprintf("LLM fix: cannot write %s: %v", relClean, err),
					"path":    relForWrite, "error": err.Error(),
				})
			}
			continue
		}
		if audit != nil {
			audit.Log(ctx, "evaluator.fix_applied", map[string]interface{}{
				"message": fmt.Sprintf("LLM fix applied to %s (step=%s).", relClean, step),
				"path":    relClean, "step": step,
			})
		}
		pathsUpdated = append(pathsUpdated, relClean)
	}
	if audit != nil && len(pathsUpdated) > 0 {
		audit.Log(ctx, "evaluator.fix_response", map[string]interface{}{
			"message":              fmt.Sprintf("LLM fix applied to %d file(s).", len(pathsUpdated)),
			"step":                 step,
			"paths_updated":        pathsUpdated,
			"fix_response_summary": pathsUpdated,
		})
	}
	// Run formatter after writing fixed files so format checks (e.g. spring-javaformat:validate) pass on re-compile.
	if len(pathsUpdated) > 0 && opts.FormatAfterFix != nil {
		if err := opts.FormatAfterFix(ctx, opts.RepoPath, pathsUpdated); err != nil {
			if errors.Is(err, ErrFormatAfterFixSkipped) {
				if audit != nil {
					audit.Log(ctx, "evaluator.format_after_fix", map[string]interface{}{
						"message": err.Error(),
					})
				}
			} else if audit != nil {
				audit.LogError(ctx, "evaluator.format_after_fix", map[string]interface{}{
					"message": fmt.Sprintf("Format after fix failed: %v", err),
					"error":   err.Error(),
				})
			}
		} else if audit != nil {
			audit.Log(ctx, "evaluator.format_after_fix", map[string]interface{}{"message": "Format applied after LLM fix."})
		}
	}
	if len(pathsUpdated) == 0 {
		return false, nil
	}
	*attemptCounter++
	return true, append([]string(nil), pathsUpdated...)
}

// chooseArtifactAmongBasenameMatches picks one repo-relative path when several generated artifacts share the same file name.
// Prefers a path that appears earliest in the test/compile error output; otherwise the first candidate (deterministic order from ArtifactPaths).
func chooseArtifactAmongBasenameMatches(candidates []string, errorOutput string) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	normErr := strings.ReplaceAll(errorOutput, "\\", "/")
	best := ""
	bestIdx := -1
	for _, c := range candidates {
		n := normalizePathForFix(c)
		idx := strings.Index(normErr, n)
		if idx < 0 {
			idx = strings.Index(normErr, filepath.Base(n))
		}
		if idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
			best = c
			bestIdx = idx
		}
	}
	if best != "" {
		return best
	}
	return candidates[0]
}

// NormalizeRepoRelPath returns a canonical repo-relative path for comparisons (Clean, forward slashes, no leading slash).
// Use from prompts and fix responses so keys like "./src/Foo.java" match "src/Foo.java".
func NormalizeRepoRelPath(p string) string {
	return normalizePathForFix(p)
}

// normalizePathForFix normalizes a repo-relative path for dedup and map lookups (forward slashes, no leading slash).
func normalizePathForFix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
}

// formatFixRequestDump returns a single string representation of the fix request for audit context_dump.
func formatFixRequestDump(step SandboxStep, errorOutput string, files map[string]string) string {
	var b strings.Builder
	b.WriteString("step: ")
	b.WriteString(string(step))
	b.WriteString("\nerror_output:\n")
	b.WriteString(errorOutput)
	b.WriteString("\n\nfiles:\n")
	for path, content := range files {
		b.WriteString("--- ")
		b.WriteString(path)
		b.WriteString(" ---\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return b.String()
}

func sortedMapKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func compileErrorTouchesArtifactScope(errorOutput string, opts EvalOptions) bool {
	if strings.TrimSpace(errorOutput) == "" {
		return true
	}
	paths := make([]string, 0, len(opts.ArtifactPaths)+16)
	paths = append(paths, opts.ArtifactPaths...)
	for _, deps := range opts.ArtifactDependencies {
		paths = append(paths, deps...)
	}
	if len(paths) == 0 {
		return false
	}
	hay := strings.ToLower(strings.ReplaceAll(errorOutput, "\\", "/"))
	for _, p := range paths {
		p = normalizePathForFix(p)
		if p == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(p)) || strings.Contains(hay, strings.ToLower(filepath.Base(p))) {
			return true
		}
	}
	return false
}
