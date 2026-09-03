package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// verifyRunner answers every step from a script. e2eRan records whether the E2E pass was reached at
// all — the fact this whole verification exists to establish.
type verifyRunner struct {
	compileOK bool
	testOK    bool
	e2eOK     bool
	e2eRan    atomic.Bool
	calls     atomic.Int32
}

func step(s evaluator.SandboxStep, ok bool, summary string) evaluator.StepResult {
	out := evaluator.StepResult{Step: s, OK: ok, Summary: summary}
	if !ok {
		out.Output = summary
	}
	return out
}

func (r *verifyRunner) Compile(context.Context, string, string) evaluator.StepResult {
	r.calls.Add(1)
	return step(evaluator.StepCompile, r.compileOK, "compile")
}
func (r *verifyRunner) Test(context.Context, string, string) evaluator.StepResult {
	return step(evaluator.StepTest, r.testOK, "FAIL src/app/a.test.ts")
}
func (r *verifyRunner) TestWithCommand(context.Context, string, string, string) evaluator.StepResult {
	return step(evaluator.StepTest, r.testOK, "FAIL src/app/a.test.ts")
}
func (r *verifyRunner) TestE2EPass(context.Context, string, string, string, string) evaluator.StepResult {
	r.e2eRan.Store(true)
	return step(evaluator.StepTestE2E, r.e2eOK, "FAIL e2e/routes/catalog.spec.ts")
}
func (r *verifyRunner) Lint(context.Context, string, string) evaluator.StepResult {
	return step(evaluator.StepLint, true, "lint")
}
func (r *verifyRunner) Coverage(context.Context, string, string) evaluator.StepResult {
	return step(evaluator.StepCoverage, true, "coverage")
}
func (r *verifyRunner) Mutation(context.Context, string, string, []string) evaluator.StepResult {
	return step(evaluator.StepMutation, true, "skipped")
}

type capturingAuditor struct{ steps []string }

func (a *capturingAuditor) Log(_ context.Context, s string, _ interface{}) {
	a.steps = append(a.steps, s)
}
func (a *capturingAuditor) LogError(_ context.Context, s string, _ interface{}) {
	a.steps = append(a.steps, s)
}
func (a *capturingAuditor) has(s string) bool {
	for _, got := range a.steps {
		if got == s {
			return true
		}
	}
	return false
}

func verifyOpts() evaluator.EvalOptions {
	o := evaluator.DefaultEvalOptions("/repo", "typescript")
	o.ArtifactPaths = []string{"src/app/a.test.ts", "src/app/b.test.ts", "e2e/routes/catalog.spec.ts"}
	o.RunE2ETestPass = true
	o.E2ETestCommand = "npx playwright test"
	return o
}

// THE SHIPPING BUG THIS CLOSES. The evaluator stops at the first failing step, so a run whose unit
// suite never passed has never executed its E2E pass — and discarding the stuck unit artifacts is
// exactly what unblocks it. The core run of 2026-09-02 shipped three generated Playwright specs on
// the strength of "the rest are green", a claim nothing had measured.
func TestVerifyAfterDiscard_runsTheStepsTheDiscardUnblocked(t *testing.T) {
	r := &verifyRunner{compileOK: true, testOK: true, e2eOK: false}
	aud := &capturingAuditor{}

	ok := verifyAfterDiscard(context.Background(), r, verifyOpts(), []string{"src/app/a.test.ts"}, aud)

	if !r.e2eRan.Load() {
		t.Fatal("the E2E pass never ran; the discard's claim was still unverified")
	}
	if ok {
		t.Error("E2E failed, so the run must not be reported stable")
	}
	if !aud.has("pipeline.post_discard_verification_fail") {
		t.Errorf("missing the failure event; audited: %v", aud.steps)
	}
}

// A tree that really is green after the discard still ships.
func TestVerifyAfterDiscard_passesWhenTheRemainderIsGreen(t *testing.T) {
	r := &verifyRunner{compileOK: true, testOK: true, e2eOK: true}
	aud := &capturingAuditor{}

	if ok := verifyAfterDiscard(context.Background(), r, verifyOpts(), []string{"src/app/a.test.ts"}, aud); !ok {
		t.Fatal("a green remainder must verify")
	}
	if !aud.has("pipeline.post_discard_verification_pass") {
		t.Errorf("missing the pass event; audited: %v", aud.steps)
	}
}

// A verification that answers a failure by deleting more of the run's own output is not a
// verification. Discard must be off for this pass.
func TestVerifyAfterDiscard_cannotDiscardAgain(t *testing.T) {
	r := &verifyRunner{compileOK: true, testOK: false, e2eOK: true}
	aud := &capturingAuditor{}

	ok := verifyAfterDiscard(context.Background(), r, verifyOpts(), []string{"src/app/a.test.ts"}, aud)

	if ok {
		t.Error("a failing remainder must not verify")
	}
	if aud.has("evaluator.fix_loop_stuck_artifact_discarded") {
		t.Errorf("the verification discarded more artifacts; audited: %v", aud.steps)
	}
}

// The discarded files are gone from disk, so the fixer must not be handed them as writable targets.
func TestWithoutDiscarded_dropsRemovedArtifacts(t *testing.T) {
	got := withoutDiscarded(
		[]string{"src/app/a.test.ts", "src/app/b.test.ts", "e2e/routes/catalog.spec.ts"},
		[]string{"./src/app/a.test.ts"}, // the discard's own spelling need not match exactly
	)
	if len(got) != 2 {
		t.Fatalf("got %v, want the two survivors", got)
	}
	for _, p := range got {
		if strings.Contains(p, "a.test.ts") {
			t.Errorf("kept a discarded artifact: %v", got)
		}
	}
	if out := withoutDiscarded(nil, []string{"x"}); out != nil {
		t.Errorf("nil in, nil out; got %v", out)
	}
}
