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
	applied, touched := applyLLMFix(ctx, opts, step, errorOutput, audit, &counter, maxAttempts, loopState, "")
	return FixStepResult{
		Attempt:          attempt,
		MaxAttempts:      maxAttempts,
		Applied:          applied,
		TouchedPaths:     touched,
		AttemptsConsumed: counter,
	}
}

// FixStepResult is the standalone result of RunFix.
type FixStepResult struct {
	Attempt          int
	MaxAttempts      int
	Applied          bool
	TouchedPaths     []string
	AttemptsConsumed int
}
