// Package evaluator validates correctness from compiler and test output (execution feedback loop).
package evaluator

import (
	"context"
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
