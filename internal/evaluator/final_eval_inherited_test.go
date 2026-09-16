package evaluator

import (
	"context"
	"testing"
)

// finalEvalRunner compiles clean and fails the test step with a fixed output.
type finalEvalRunner struct{ testOut string }

func (r *finalEvalRunner) Compile(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepCompile, OK: true, Summary: "ok"}
}
func (r *finalEvalRunner) Test(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepTest, OK: false, Summary: "fail", Output: r.testOut}
}
func (r *finalEvalRunner) Lint(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepLint, OK: true}
}
func (r *finalEvalRunner) Coverage(ctx context.Context, repoPath, lang string) StepResult {
	return StepResult{Step: StepCoverage, OK: true}
}
func (r *finalEvalRunner) Mutation(ctx context.Context, repoPath, lang string, m []string) StepResult {
	return StepResult{Step: StepMutation, OK: true, Summary: "skipped"}
}

var _ SandboxRunner = (*finalEvalRunner)(nil)

// finalTestRow returns the last audit payload for the final evaluation's TEST step.
//
// The two repositories spell that row differently — "evaluator.test" here, "evaluator.final.test" in
// asqs-core — and the name is not what this file is testing. Matching either keeps one test file
// valid in both.
func finalTestRow(t *testing.T, audit *recordingAuditor) map[string]interface{} {
	t.Helper()
	for _, key := range []string{"evaluator.test", "evaluator.final.test"} {
		if rows := audit.payloads[key]; len(rows) > 0 {
			return rows[len(rows)-1]
		}
	}
	t.Fatalf("no final test-step row recorded; steps=%v errors=%v", audit.steps, audit.errorSteps)
	return nil
}

// Run api-aace45f1f78d36a4474c06ad7d7ffae3 failed every one of its nine iterations at COMPILE, so
// the evaluation loop's test branch — where the inherited-failure comparison was wired — never ran.
// The one test failure of the whole run came from the final evaluation after the discard, which
// takes a different path entirely, and no failure_inherited field appeared anywhere in the log.
//
// The final evaluation is where a run's verdict is actually decided. It has to answer the question
// too.
func TestRunFinalEval_reportsAnInheritedTestFailure(t *testing.T) {
	const out = `[xUnit.net 00:00:00.19]     App.FunctionalTests.SeedDataTests.Seeds [FAIL]
[xUnit.net 00:00:00.19]       System.ArgumentNullException : Value cannot be null. (Parameter 'connectionString')
Failed!  - Failed: 1, Passed: 4, Skipped: 0, Total: 5
`
	opts := EvalOptions{Lang: "csharp", RepoPath: t.TempDir(), TestCommand: "dotnet test"}
	opts.BaselineTestSignature = FailureSignature(opts.Lang, StepTest, out)

	audit := &recordingAuditor{}
	res := RunRunFinalEval(context.Background(), &finalEvalRunner{testOut: out}, opts, audit)

	if res.TestFailureInherited != true {
		t.Error("the final evaluation must report that this failure was inherited")
	}
	row := finalTestRow(t, audit)
	got, ok := row["failure_inherited"].(bool)
	if !ok {
		t.Fatalf("the final test-step row carries no failure_inherited field: %v", row)
	}
	if !got {
		t.Error("failure_inherited is false for the baseline's own failure")
	}
}

// A failure the run caused must not be excused as one it inherited.
func TestRunFinalEval_doesNotClaimAnIntroducedFailureIsInherited(t *testing.T) {
	const baselineOut = `System.ArgumentException : Format of the initialization string does not conform to specification.
Failed!  - Failed: 1, Passed: 4, Skipped: 0, Total: 5
`
	const runOut = `System.NullReferenceException : Object reference not set to an instance of an object.
Failed!  - Failed: 1, Passed: 4, Skipped: 0, Total: 5
`
	opts := EvalOptions{Lang: "csharp", RepoPath: t.TempDir(), TestCommand: "dotnet test"}
	opts.BaselineTestSignature = FailureSignature(opts.Lang, StepTest, baselineOut)

	audit := &recordingAuditor{}
	res := RunRunFinalEval(context.Background(), &finalEvalRunner{testOut: runOut}, opts, audit)

	if res.TestFailureInherited {
		t.Error("a different failure must not be reported as inherited")
	}
	if got, _ := finalTestRow(t, audit)["failure_inherited"].(bool); got {
		t.Error("failure_inherited is true for a failure the baseline never saw")
	}
}

// With no baseline signature the field must still be present and false: the reader has to be able
// to tell "not inherited" from "nobody asked".
func TestRunFinalEval_reportsNotInheritedWithoutABaseline(t *testing.T) {
	opts := EvalOptions{Lang: "csharp", RepoPath: t.TempDir(), TestCommand: "dotnet test"}
	audit := &recordingAuditor{}
	res := RunRunFinalEval(context.Background(), &finalEvalRunner{testOut: "boom\n"}, opts, audit)

	if res.TestFailureInherited {
		t.Error("no baseline signature: nothing to inherit from")
	}
	if _, ok := finalTestRow(t, audit)["failure_inherited"]; !ok {
		t.Error("failure_inherited must be present even when false")
	}
}
