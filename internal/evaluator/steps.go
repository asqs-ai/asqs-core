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
		WidenPending:     loopState != nil && loopState.widenNextRound,
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
	// WidenPending is true when this round's outcome queued a scope widening for the next round
	// (FixLoopState.widenNextRound): the next round will offer every writable artifact, so a
	// caller counting consecutive unusable responses must not count this one as a repeat.
	WidenPending bool
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
	// FixSkipTestOutsideWritableScope: the test step failed, but its output attributes the failure
	// to no file the fixer may write and cites no source location at all. Terminal: a repair round
	// cannot change a failure that names nothing it is allowed to touch. The case that motivated it
	// is vitest's "No test files found, exiting with code 1" after a discard (asqs-go run
	// api-72dad6bb281cacee338f43c48432a780), which the fixer was asked to repair three times and
	// finally answered by rewriting an unrelated Playwright spec.
	FixSkipTestOutsideWritableScope = "test_failure_outside_writable_scope"
	// FixSkipPrimarySiteNeverTouched means StopPrimarySiteAfterUntouchedRounds consecutive rounds
	// edited around the blamed site (line or, for resolution failures, the import block) against
	// an unchanged line-insensitive failure signature — including one round that was FORCED onto
	// that file alone with an explicit directive. Terminal: the model has demonstrated it will not
	// perform this specific repair. Ported from asqs-go.
	FixSkipPrimarySiteNeverTouched = "fix_primary_site_never_touched"
)

// IsTerminalFixSkip reports whether a FixSkip* reason means the fixer is out of road for this step,
// as opposed to having produced one unusable turn.
//
// The taxonomy above was documented from the day the constants were added but had no predicate, so
// every call site discarded the reason with `_` and could not act on the distinction. The compile
// branch in particular then had no way to tell "retry, a fresh turn may work" from "nothing further
// will ever be attempted for this step".
//
// FixSkipNoAcceptedWrites is deliberately NOT terminal: every write the model proposed was refused
// by a quality gate this round (empty file, deleted tests, syntactic shell), and a different turn
// can propose something the gates accept.
func IsTerminalFixSkip(reason string) bool {
	switch reason {
	case FixSkipLoopRepeat, FixSkipLoopOscillation, FixSkipLoopNoProgress, FixSkipNoWritableArtifacts, FixSkipPrimarySiteNeverTouched:
		return true
	default:
		return false
	}
}
