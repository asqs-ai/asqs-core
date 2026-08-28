// Package evaluator validates correctness from compiler and test output (execution feedback loop).
package evaluator

import (
	"context"
	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"time"
)

// SandboxStep is a single step in the evaluation pipeline (build, test, lint, coverage, mutation).
type SandboxStep string

const (
	StepCompile  SandboxStep = "compile"
	StepTest     SandboxStep = "test"
	StepTestE2E  SandboxStep = "test_e2e" // second test pass (Playwright/Cypress) after unit tests for JS/TS
	StepLint     SandboxStep = "lint"
	StepCoverage SandboxStep = "coverage"
	StepMutation SandboxStep = "mutation" // optional, for critical modules
)

// TestWithCommandRunner is optional: run the test step with an explicit shell command (dual unit vs E2E evaluation).
type TestWithCommandRunner interface {
	TestWithCommand(ctx context.Context, repoPath, lang, testCommand string) StepResult
}

// CompileWithCommandRunner is optional: run the compile step with an explicit shell command. Used by the
// evaluator's scoped-compile fallback: when a full-solution build fails because an unrelated project in the
// same .sln can't restore its (e.g. private/authenticated) NuGet feed, the evaluator can retry with a
// command scoped to just the artifact's consumer project so the restore graph excludes the failing sibling.
type CompileWithCommandRunner interface {
	CompileWithCommand(ctx context.Context, repoPath, lang, compileCommand string) StepResult
}

// EvalWorkSubpathReporter is optional: expose the repo-relative sub-directory that the sandbox uses as the
// toolchain working directory (mono-repo workspace). When present, callers that construct ad-hoc shell
// commands with paths can rewrite repo-relative paths into paths that resolve against that cwd — MSBuild in
// particular fails with MSB1009 when fed a repo-relative `.csproj` while running from a mono-repo subpath.
// Returning "" means the toolchain runs from the repo root and no path rewriting is needed.
type EvalWorkSubpathReporter interface {
	ReportEvalWorkSubpath() string
}

// E2EPassDockerRunner is optional: run the E2E test pass with an explicit command; Docker runners may use a Playwright-capable image for JS/TS Playwright/Cypress.
type E2EPassDockerRunner interface {
	TestE2EPass(ctx context.Context, repoPath, lang, testCommand, e2eFramework string) StepResult
}

// CoverageWithCommandRunner is optional: run coverage using a specific test command (typically unit tests).
type CoverageWithCommandRunner interface {
	CoverageWithCommand(ctx context.Context, repoPath, lang, testCommand string) StepResult
}

// StepResult is the outcome of one sandbox step.
type StepResult struct {
	Step    SandboxStep
	OK      bool
	Output  string
	Summary string // short message for logs/audit
	Err     error

	// Started is the wall-clock instant (UTC) the step began executing. Zero when the step was
	// skipped (e.g. compile_once_per_eval skip, lint disabled) or constructed by a legacy code
	// path that did not measure. Always set by RunEvaluation when the step actually invoked the
	// SandboxRunner. (A.6 — tool attempt durations.)
	Started time.Time
	// DurationMs is the wall-clock cost in milliseconds. Same zero-means-not-measured semantics
	// as Started. Includes the runner round-trip but excludes any post-processing the evaluator
	// does after the runner returns. Persisted by session/engine into session_attempts.duration_ms.
	DurationMs int64
}

// FixAction is the recommended action when a step fails (used in the evaluation loop).
type FixAction string

const (
	FixNone         FixAction = ""
	FixImportsMocks FixAction = "fix_imports_mocks"  // compile failed
	FixAssumptions  FixAction = "adjust_assumptions" // tests failed
	FixStabilize    FixAction = "stabilize"          // flaky: stabilize or downgrade to unit test
)

// EvalResult is the aggregate result of the evaluation workflow for one artifact (e.g. one generated test file).
type EvalResult struct {
	ArtifactPath string       // path to the generated file
	Steps        []StepResult // compile, test, lint, coverage, optional mutation
	FixAction    FixAction    // recommended fix if failed
	Stable       bool         // true if all steps passed and no flakiness detected
	Iterations   int          // number of loop iterations (fix attempts) performed
}

// FixRequest is the input for an LLM fix: failed step, error output, relevant file contents, and metadata for best results.
type FixRequest struct {
	Step        SandboxStep       // compile or test
	ErrorOutput string            // full compiler or test failure output
	Files       map[string]string // repo-relative path -> file content (generated or relevant code)
	// ArtifactPaths lists repo-relative paths of generated test files to fix (same as EvalOptions.ArtifactPaths). Used to order context (artifacts first) and to match response paths.
	ArtifactPaths []string
	// ArtifactContexts carries optional per-artifact retrieval/generation context (dependency graph, fixtures/config, branch-gap hints)
	// keyed by artifact path. Fixers can use this to preserve the original test intent during repairs.
	ArtifactContexts map[string]string
	RepoPath         string // absolute repo root
	Lang             string // e.g. "java", "javascript", "typescript"
	// TestFramework is the detected test framework (e.g. "jest", "jasmine", "mocha", "junit"). Empty for unknown. Helps the LLM use correct syntax and assertions.
	TestFramework string
	// BuildTool is the build tool in use (e.g. "mvn", "gradle", "npm"). Empty when not set.
	BuildTool string
	// CompileCommand is the exact compile command used (e.g. "./mvnw compile -q -B"). Empty when not set.
	CompileCommand string
	// TestCommand is the exact test command used (e.g. "./mvnw test -q -B"). Empty when not set.
	TestCommand string
	// Manifests are dependency manifest files (e.g. package.json, pom.xml) so the LLM only suggests imports/packages that exist in the project. Key = repo-relative path (e.g. "package.json"); value = file content.
	Manifests map[string]string
	// APISurface carries real member signatures for the third-party types the diagnostic blamed,
	// read from the project's compile classpath. Empty when no provider is configured, when the
	// classpath could not be resolved, or when the failure named no third-party type.
	//
	// This exists because the fixer's success rate partitioned exactly on whether the repair was
	// inferable from information already in the prompt: every missing-import error was fixed, and
	// every wrong-third-party-API error (hasURLContaining, hasStatus, assertThat-with-a-lambda) was
	// re-emitted unchanged for four rounds. The signatures are in the jars Maven already
	// downloaded; nothing else in the prompt carries them.
	APISurface []apisurface.TypeSurface
	// MissingMemberFacts are deterministic, compiler-derived statements for methods the diagnostic
	// rejected on REPO-OWNED types ("Vets has NO method setVets; declared methods: getVetList"),
	// including static-import candidates for bare calls in the test class itself. They cover the
	// half of "cannot find symbol: method" that the classpath APISurface deliberately does not
	// (FilterOwnedTypes) — see fix_missing_members.go. Rendered by every builder as a block the
	// model must treat as ground truth.
	MissingMemberFacts []string
	// AbsentSymbols are the type names the same classpath scan looked up and did NOT find — the
	// negative half of APISurface, and the half that used to be discarded.
	//
	// A resolved surface tells the model where a type lives; nothing told it that a type lives
	// nowhere. In run api-0c344e6bc0658e0db06506efb9d964f5 MockBean (removed in Spring Boot 4) and
	// MockMvcRestServiceServer (never a real class) were looked up on all ten rounds, resolved on
	// none, and were never mentioned in a prompt — so the model reintroduced them every round and
	// they stayed in the diagnostic that produced the next round's targets. Only populated when at
	// least one sibling target DID resolve, which is what separates "absent" from "classpath
	// unreadable".
	AbsentSymbols []string
	// TestFailureFacts are deterministic statements derived from RUNTIME test failures plus the
	// test class's own source — today, Mockito misuse: when()/given() on a receiver the test
	// provably constructs with `new`, and stubbings the tested code never consumes. They exist
	// because these defects survived six fixer rounds in run api-12aa1935d113c9ea8b50a516fd275660
	// with the exception text and the production bodies both in the prompt: the model kept
	// repairing around the misuse instead of naming it. Empty for compile steps and for failures
	// the parser cannot prove — see fix_mockito_facts.go. Rendered beside MissingMemberFacts, in a
	// separate block because these are runtime-verified, not compiler-verified.
	TestFailureFacts []string
	// ErrorSummary is an LLM-written summary of an oversized error log, attached ALONGSIDE the raw
	// text rather than replacing it ("dependencies" prose can be wrong, and the raw text stays
	// authoritative). Empty when the log is small, the feature is off, or no summarizer is wired.
	// Before this field existed the summary was computed for every oversized log and then shown
	// only to audit readers, while the model worked from a head+tail gist that had dropped the
	// failures the summary named.
	ErrorSummary string
	// PriorAttempts is the compacted record of fixer rounds already completed for this step in this
	// run: what actually landed on disk, what was skipped and why, and the failure signature that
	// came back afterwards. Empty on the first round.
	//
	// It exists because llmfix's raw multi-turn history cannot survive a real fix prompt: retention
	// drops message pairs above a 64k budget and one prompt in the motivating upstream run was
	// 141-147k runes, so every round was stateless and rounds 3 and 4 produced byte-identical
	// output.
	PriorAttempts []FixAttemptRecord
	// FixAttempt is the current fix attempt (1-based). When > 1, the LLM can try a different strategy. 0 = unknown.
	FixAttempt int
	// MaxFixAttempt is the max fix attempts for this step (e.g. 3). 0 = unknown.
	MaxFixAttempt int
	// InfrastructureFailureKind is set when errclass classified the failure as environment/infrastructure (e.g. sqlite_connection_string). Empty when not classified.
	InfrastructureFailureKind string
	// GapSessionID scopes multi-turn fixer conversation state when gap_concurrency > 1.
	GapSessionID string
}

// FixAttemptAutoEscalationThreshold is the (1-based) fix attempt at which context-hygiene flags
// that are otherwise opt-in (`runner.fixer_dependency_signature_only`,
// `runner.fixer_structured_user_message`) are auto-forced regardless of YAML/env defaults. The
// rationale — documented in DOCUMENTATION.md ("Automatic context-hygiene escalation") — is that a
// fix loop that has already burned two attempts on the same failure rarely converges by re-sending
// the same prompt shape a third time; flipping to signature-sliced read-only deps + XML-framed
// user messages changes what the LLM sees without needing a new capability. Exported so the
// llmfix.Fixer can check the same threshold when deciding whether to break out of multi-turn
// conversation history.
const FixAttemptAutoEscalationThreshold = 3

// FixResponse is the LLM fix output: updated content for files to apply.
type FixResponse struct {
	Files map[string]string // repo-relative path -> new full file content (only keys that changed)
	// Edits is the preferred, targeted form: repo-relative path -> ordered search/replace edits.
	// When non-empty it takes precedence over Files for those paths.
	//
	// Whole-file regeneration was the fixer's only unit of work, and it is why a one-token defect
	// survived seven rounds. To change `Set<Visit>` to `Collection<Visit>` on line 32 the model had
	// to reproduce two entire files (~140 lines) from scratch every round, so:
	//
	//   - nothing forced the change through — line 32 came back byte-identical seven times while
	//     the fixer "successfully applied" that file each round;
	//   - every regeneration could introduce new damage, and did (a fresh error appeared on line 33
	//     in round 2, created by a round meant to be repairing);
	//   - each rewrite re-picked the third-party API independently, producing a 2-cycle
	//     (hasHeader -> assertThat(Map) -> hasHeader -> …) rather than convergence.
	//
	// An edit either matches its anchor and changes the file, or it does not match and says so.
	// That is the property whole-file rewriting cannot provide.
	Edits map[string][]FixEdit
}

// FixEdit is one exact-string search/replace within a file.
//
// Anchored on content rather than line numbers on purpose: line numbers drift the moment any
// earlier edit lands, and the fixer applies several edits per file per round.
type FixEdit struct {
	// Find is the exact snippet to locate. Must appear exactly once in the file.
	Find string `json:"find"`
	// Replace is what replaces it. Empty means deletion.
	Replace string `json:"replace"`
}

// Fixer is called when compile or test fails during evaluation. It receives the error and code and returns fixed file contents to apply. Optional; max attempts (e.g. 3) are enforced by the evaluator.
type Fixer interface {
	Fix(ctx context.Context, req FixRequest) (FixResponse, error)
}

// FixRequestIntrospector is an optional interface a Fixer may implement to expose audit-friendly
// configuration hints (e.g. whether multi-turn repair is active, whether the provider will be asked
// for structured JSON output). The evaluator surfaces these keys in the `evaluator.fix_request`
// audit payload so operators can correlate LLM behaviour with the prompt they're reading without
// having to run a separate inspection pass. Fixers that do not implement this interface get no
// extra keys — the fields are simply omitted. Implementations must be safe to call concurrently
// with Fix and must return only small, JSON-marshalable values (bool / string / int).
type FixRequestIntrospector interface {
	FixRequestAuditMetadata() map[string]any
}

// SandboxRunner runs build/test/lint/coverage/mutation in a sandbox (e.g. Docker).
// Implementations execute the actual commands and return step results.
type SandboxRunner interface {
	// Compile builds/compiles the project in repoPath. Returns StepResult for StepCompile.
	Compile(ctx context.Context, repoPath, lang string) StepResult
	// Test runs the test suite (e.g. mvn test, dotnet test).
	Test(ctx context.Context, repoPath, lang string) StepResult
	// Lint runs lint/format checks (e.g. spotless, dotnet format).
	Lint(ctx context.Context, repoPath, lang string) StepResult
	// Coverage runs tests with coverage and returns delta vs baseline (if available).
	Coverage(ctx context.Context, repoPath, lang string) StepResult
	// Mutation runs mutation tests for critical modules; optional, may return OK with Summary "skipped".
	Mutation(ctx context.Context, repoPath, lang string, criticalModules []string) StepResult
}
