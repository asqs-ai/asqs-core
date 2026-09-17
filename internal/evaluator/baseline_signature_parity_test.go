package evaluator

import (
	"context"
	"strings"
	"testing"
)

// baselineThenRunRunner answers the baseline's test call with one log and every later call with
// another: the same failures, plus the passing tests the run went on to write.
type baselineThenRunRunner struct {
	baselineOut, afterOut string
	testCalls             int
}

func (r *baselineThenRunRunner) Compile(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepCompile, OK: true, Summary: "ok"}
}
func (r *baselineThenRunRunner) Test(ctx context.Context, repoPath, lang string) StepResult {
	r.testCalls++
	out := r.afterOut
	if r.testCalls == 1 {
		out = r.baselineOut
	}
	return StepResult{Step: StepTest, OK: false, Summary: "fail", Output: out}
}
func (r *baselineThenRunRunner) Lint(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepLint, OK: true}
}
func (r *baselineThenRunRunner) Coverage(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepCoverage, OK: true}
}
func (r *baselineThenRunRunner) Mutation(ctx context.Context, repoPath, lang string, m []string) StepResult {
	return StepResult{Step: StepMutation, OK: true, Summary: "skipped"}
}

var _ SandboxRunner = (*baselineThenRunRunner)(nil)

// End to end over the two halves that must agree: CaptureBaselineFailures records the signature and
// the final evaluation compares against it.
//
// Run api-60c974ecfa42c5bee4930a7e590e4548 is what happens when they do not. The baseline hashed a
// 1049-line log, the final evaluation hashed a 1060-line one, and the eleven extra lines were the
// passing tests the run had just written. All five evaluator.test rows reported
// failure_inherited=false for three failures in two files the run never authored.
func TestBaselineToFinalEval_aninheritedFailureSurvivesTheRunAddingTests(t *testing.T) {
	blocks := "[xUnit.net 00:00:03.10]     App.Tests.ApiEndpoints.ContributorList.ReturnsTwo [FAIL]\n" +
		"[xUnit.net 00:00:03.10]       System.ArgumentException : Format of the initialization string does not conform.\n"
	baselineOut := "Test run for /workspace/tests/App.Tests/bin/Release/net10.0/App.Tests.dll\n" +
		blocks + "Failed!  - Failed: 3, Passed: 6, Skipped: 1, Total: 10\n"
	afterOut := "Test run for /workspace/tests/App.Tests/bin/Release/net10.0/App.Tests.dll\n" +
		"[xUnit.net 00:00:02.01]     App.Tests.SeedDataTests.Seeds [PASS]\n" +
		"[xUnit.net 00:00:02.40]     App.Tests.ExtensionsTests.Maps [PASS]\n" +
		blocks + "Failed!  - Failed: 3, Passed: 17, Skipped: 1, Total: 21\n"

	runner := &baselineThenRunRunner{baselineOut: baselineOut, afterOut: afterOut}
	opts := EvalOptions{Lang: "csharp", RepoPath: t.TempDir(), TestCommand: "dotnet test"}

	base := CaptureBaselineFailures(context.Background(), runner, opts)
	if !base.TestsCaptured || base.TestsClean {
		t.Fatalf("baseline did not capture a failing test run: %+v", base)
	}
	if strings.TrimSpace(base.TestSignature) == "" {
		t.Fatal("baseline recorded no test signature")
	}

	opts.BaselineTestSignature = base.TestSignature
	res := RunRunFinalEval(context.Background(), runner, opts, nil)
	if !res.TestFailureInherited {
		t.Error("the baseline's own failure was not recognised once the run added passing tests")
	}
}
