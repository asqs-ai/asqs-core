package evaluator

import (
	"context"
	"strings"
)

// RunCompile executes the compile step and stamps duration metadata.
func RunCompile(ctx context.Context, runner SandboxRunner, opts EvalOptions) StepResult {
	start := nowFunc()
	return stampDuration(runner.Compile(ctx, opts.RepoPath, opts.Lang), start)
}

// RunTest executes the unit test step and honors TestWithCommandRunner when a command is set.
func RunTest(ctx context.Context, runner SandboxRunner, opts EvalOptions, testCmd string) StepResult {
	cmd := strings.TrimSpace(testCmd)
	start := nowFunc()
	if tc, ok := runner.(TestWithCommandRunner); ok && cmd != "" {
		return stampDuration(tc.TestWithCommand(ctx, opts.RepoPath, opts.Lang, cmd), start)
	}
	return stampDuration(runner.Test(ctx, opts.RepoPath, opts.Lang), start)
}

// RunTestE2E executes the E2E test step and honors E2EPassDockerRunner when available.
func RunTestE2E(ctx context.Context, runner SandboxRunner, opts EvalOptions, testCmd string) StepResult {
	cmd := strings.TrimSpace(testCmd)
	if e2e, ok := runner.(E2EPassDockerRunner); ok && cmd != "" {
		start := nowFunc()
		return stampDuration(e2e.TestE2EPass(ctx, opts.RepoPath, opts.Lang, cmd, strings.TrimSpace(opts.E2EFramework)), start)
	}
	return RunTest(ctx, runner, opts, testCmd)
}

// RunLint executes the lint/format-check step and stamps duration metadata.
func RunLint(ctx context.Context, runner SandboxRunner, opts EvalOptions) StepResult {
	start := nowFunc()
	return stampDuration(runner.Lint(ctx, opts.RepoPath, opts.Lang), start)
}

// RunCoverage executes coverage and honors CoverageWithCommandRunner when a unit command is set.
func RunCoverage(ctx context.Context, runner SandboxRunner, opts EvalOptions, testCmd string) StepResult {
	cmd := strings.TrimSpace(testCmd)
	start := nowFunc()
	if tc, ok := runner.(CoverageWithCommandRunner); ok && cmd != "" {
		return stampDuration(tc.CoverageWithCommand(ctx, opts.RepoPath, opts.Lang, cmd), start)
	}
	return stampDuration(runner.Coverage(ctx, opts.RepoPath, opts.Lang), start)
}

// RunMutation executes the optional mutation step and stamps duration metadata.
func RunMutation(ctx context.Context, runner SandboxRunner, opts EvalOptions) StepResult {
	start := nowFunc()
	return stampDuration(runner.Mutation(ctx, opts.RepoPath, opts.Lang, opts.CriticalModules), start)
}

// RunFix executes one LLM repair attempt for a failed sandbox step and writes accepted fixes.
func RunFix(ctx context.Context, opts EvalOptions, step SandboxStep, errorOutput string, audit Auditor, attempt, maxAttempts int, loopState *FixLoopState) FixStepResult {
	if attempt < 1 {
		attempt = 1
	}
	if maxAttempts < attempt {
		maxAttempts = attempt
	}
	counter := attempt - 1
	applied, touched, skipReason := applyLLMFix(ctx, opts, step, errorOutput, audit, &counter, maxAttempts, loopState, "")
	return FixStepResult{
		Attempt:          attempt,
		MaxAttempts:      maxAttempts,
		Applied:          applied,
		TouchedPaths:     touched,
		AttemptsConsumed: counter,
		SkippedReason:    skipReason,
		Retryable:        skipReason == FixSkipResponseUnusable,
	}
}

// FixStepResult is the standalone result of RunFix.
type FixStepResult struct {
	Attempt          int
	MaxAttempts      int
	Applied          bool
	TouchedPaths     []string
	AttemptsConsumed int
	// SkippedReason is one of the FixSkip* constants when Applied is false; empty on success.
	SkippedReason string
	// Retryable is true when SkippedReason indicates a bad model turn rather than an exhausted
	// fixer. A caller can keep going (bounded) instead of terminating the run — before the split,
	// ANY no-write outcome was terminal, so one unparseable JSON response ended a run with a
	// non-compiling tree and max_fix_attempt=1 in the audit.
	Retryable bool
}

// Fix-skip reasons: why a fix round produced no write, split by whether a fresh turn could still
// help.
//
//	retryable — the model's output was unusable this turn (bad JSON, empty object, LLM error).
//	            A fresh turn may well succeed; ending the run here wastes the remaining budget.
//	terminal  — the fixer is out of road (a circuit-breaker tripped, nothing writable in scope).
const (
	FixSkipResponseUnusable = "fixer_response_unusable"
	// FixSkipLoopRepeat / FixSkipLoopOscillation / FixSkipLoopNoProgress are the three ways the
	// circuit-breaker gives up, and they are distinct on purpose.
	//
	// All three used to be reported as one name, so an audit could read "Fix loop oscillating:
	// previously-seen error signatures reappeared 2 time(s)" on one line and a generic
	// "fix_loop_repeat" on the next — two names for one event, and the name an operator sees was
	// not the breaker they would need to tune. All three remain terminal.
	FixSkipLoopRepeat          = "fix_loop_repeat"
	FixSkipLoopOscillation     = "fix_loop_oscillation"
	FixSkipLoopNoProgress      = "fix_loop_no_progress"
	FixSkipNoWritableArtifacts = "no_writable_artifacts"
	FixSkipNoAcceptedWrites    = "no_accepted_writes"
)
