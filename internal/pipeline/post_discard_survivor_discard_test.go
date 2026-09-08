package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// survivorRunner is verifyRunner whose E2E pass fails a fixed number of times and then passes,
// which is what a tree looks like once the failing spec is off disk.
type survivorRunner struct {
	verifyRunner
	e2eFailuresLeft atomic.Int32
	e2eCalls        atomic.Int32
}

func (r *survivorRunner) TestE2EPass(context.Context, string, string, string, string) evaluator.StepResult {
	r.e2eRan.Store(true)
	r.e2eCalls.Add(1)
	if r.e2eFailuresLeft.Add(-1) >= 0 {
		return step(evaluator.StepTestE2E, false, "  8 failed\n    [chromium] › routes/catalog.spec.ts:4:7 › GET /health\n    TypeError: apiRequestContext.get: Invalid URL")
	}
	return step(evaluator.StepTestE2E, true, "e2e ok")
}

// survivorRepo stages the surviving artifacts on disk so the discard has something to remove.
func survivorRepo(t *testing.T) (string, evaluator.EvalOptions) {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"src/app/b.test.ts", "e2e/routes/catalog.spec.ts"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("// generated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := verifyOpts()
	opts.RepoPath = dir
	return dir, opts
}

// The asqs-go run of 2026-09-08 (api-a716cb7b25db5a880b4675a04b0b08c3): the unit discard went
// green, the never-executed E2E survivors failed every repair round on a failure no spec edit can
// fix, and the whole run was reported unstable. A survivor the verification's own failure names is
// the case the first discard answered; it gets the same answer, and the remainder is verified once
// more with no repair.
func TestVerifyAfterDiscard_discardsFailingSurvivorsAndVerifiesTheRest(t *testing.T) {
	dir, opts := survivorRepo(t)
	r := &survivorRunner{verifyRunner: verifyRunner{compileOK: true, testOK: true}}
	r.e2eFailuresLeft.Store(int32(PostDiscardRepairBudget)) // every iteration of the budget, then green
	aud := &capturingAuditor{}

	ok, extra := verifyAfterDiscard(context.Background(), r, opts, []string{"src/app/a.test.ts"}, aud)

	if !ok {
		t.Fatalf("the tree without the failing spec verified; audited: %v", aud.steps)
	}
	if len(extra) != 1 || extra[0] != "e2e/routes/catalog.spec.ts" {
		t.Errorf("survivor discard = %v, want the one attributed spec", extra)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e/routes/catalog.spec.ts")); !os.IsNotExist(err) {
		t.Error("the discarded spec is still on disk")
	}
	if _, err := os.Stat(filepath.Join(dir, "src/app/b.test.ts")); err != nil {
		t.Error("the passing unit survivor must stay on disk")
	}
	if !aud.has("pipeline.post_discard_survivors_discarded") || !aud.has("pipeline.post_discard_verification_pass") {
		t.Errorf("the survivor discard and the pass must both be audited: %v", aud.steps)
	}
	if got := r.e2eCalls.Load(); got != int32(PostDiscardRepairBudget)+1 {
		t.Errorf("E2E ran %d times, want the budget's %d plus the one verification after the survivor discard", got, PostDiscardRepairBudget+1)
	}
}

// One extra verification, no repair: a tree that still fails after the survivor discard is not
// stable, and the discard is still reported because the file is gone.
func TestVerifyAfterDiscard_survivorDiscardGetsOneVerification(t *testing.T) {
	_, opts := survivorRepo(t)
	r := &survivorRunner{verifyRunner: verifyRunner{compileOK: true, testOK: true}}
	r.e2eFailuresLeft.Store(100)
	aud := &capturingAuditor{}

	ok, extra := verifyAfterDiscard(context.Background(), r, opts, []string{"src/app/a.test.ts"}, aud)

	if ok {
		t.Error("a remainder that still fails must not verify")
	}
	if len(extra) != 1 {
		t.Errorf("survivor discard = %v, want the spec reported as discarded", extra)
	}
	if !aud.has("pipeline.post_discard_verification_fail") {
		t.Errorf("the failure must be audited: %v", aud.steps)
	}
}

// Extended files are the repository's own; discard is os.Remove, so they are never discarded — the
// same protection the first tier applies.
func TestVerifyAfterDiscard_neverDiscardsAnExtendedSurvivor(t *testing.T) {
	dir, opts := survivorRepo(t)
	opts.ExtendedArtifactPaths = []string{"e2e/routes/catalog.spec.ts"}
	r := &survivorRunner{verifyRunner: verifyRunner{compileOK: true, testOK: true}}
	r.e2eFailuresLeft.Store(100)
	aud := &capturingAuditor{}

	ok, extra := verifyAfterDiscard(context.Background(), r, opts, []string{"src/app/a.test.ts"}, aud)

	if ok || len(extra) != 0 {
		t.Errorf("ok=%v extra=%v, want no discard of an extended file", ok, extra)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e/routes/catalog.spec.ts")); err != nil {
		t.Error("the extended spec must stay on disk")
	}
	if aud.has("pipeline.post_discard_survivors_discarded") {
		t.Errorf("no survivor discard may be audited: %v", aud.steps)
	}
}

// When every survivor fails there is nothing to keep: unstable, nothing removed.
func TestVerifyAfterDiscard_keepsEverythingWhenEverySurvivorFails(t *testing.T) {
	dir, opts := survivorRepo(t)
	opts.ArtifactPaths = []string{"src/app/a.test.ts", "e2e/routes/catalog.spec.ts"} // only the spec survives
	r := &survivorRunner{verifyRunner: verifyRunner{compileOK: true, testOK: true}}
	r.e2eFailuresLeft.Store(100)
	aud := &capturingAuditor{}

	ok, extra := verifyAfterDiscard(context.Background(), r, opts, []string{"src/app/a.test.ts"}, aud)

	if ok || len(extra) != 0 {
		t.Errorf("ok=%v extra=%v, want nothing discarded", ok, extra)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e/routes/catalog.spec.ts")); err != nil {
		t.Error("the only survivor must stay on disk")
	}
}
