package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArtifact(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("// generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPathLooksLikeE2EArtifact(t *testing.T) {
	for rel, want := range map[string]bool{
		"e2e/routes/home.spec.tsx":          true,
		"apps/web/e2e/login.spec.ts":        true,
		"cypress/e2e/cart.cy.ts":            true,
		"tests/playwright/checkout.spec.ts": true,
		"src/checkout.e2e.ts":               true,
		"src/pages/HomePage.test.tsx":       false,
		"src/lib/validation.test.ts":        false,
		"src/services/e2eClient.test.ts":    false,
		"__tests__/asqs-bootstrap.test.ts":  false,
		"":                                  false,
	} {
		if got := pathLooksLikeE2EArtifact(rel); got != want {
			t.Errorf("pathLooksLikeE2EArtifact(%q) = %v, want %v", rel, got, want)
		}
	}
}

// After a discard the unit artifacts are gone from disk while the E2E specs remain; only what is
// actually on disk may count as "the runner should have seen this".
func TestGeneratedUnitArtifactsOnDisk_ignoresE2EAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "src/app/router.test.tsx")
	writeArtifact(t, dir, "e2e/routes/home.spec.tsx")
	opts := DefaultEvalOptions(dir, "typescript")
	opts.ArtifactPaths = []string{"src/app/router.test.tsx", "src/pages/HomePage.test.tsx", "e2e/routes/home.spec.tsx"}
	got := generatedUnitArtifactsOnDisk(opts)
	if len(got) != 1 || got[0] != "src/app/router.test.tsx" {
		t.Fatalf("want only the unit artifact that exists; got %v", got)
	}
}

// THE POST-DISCARD CASE. Every unit artifact was discarded, only the two Playwright specs remain,
// vitest exits 1 with "No test files found". That is not a failure of anything this run can repair:
// the unit step passes and the loop moves on to the E2E pass.
func TestRunEvaluation_noTestFiles_passesWhenOnlyE2EArtifactsRemain(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "e2e/routes/home.spec.tsx")
	runner := &stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true, Summary: "compile ok"},
		test:     StepResult{Step: StepTest, OK: true, Summary: "tests ok" + NoTestFilesSuffix, Output: "No test files found, exiting with code 1"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}
	opts := DefaultEvalOptions(dir, "typescript")
	opts.ArtifactPaths = []string{"src/app/router.test.tsx", "e2e/routes/home.spec.tsx"} // unit one discarded: not on disk
	audit := &recordingAuditor{}
	res, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stable {
		t.Fatalf("an empty unit pass with only E2E artifacts on disk must be stable; steps=%s", StepSummary(res.StepResults))
	}
	if !audit.hasStep("evaluator.no_test_files_accepted") {
		t.Error("the accepted empty pass must be audited")
	}
	if audit.hasStep("evaluator.no_test_files_with_generated_tests") {
		t.Error("no generated unit test is on disk; the override must not fire")
	}
}

// THE MISCONFIGURATION CASE. Generated unit tests are on disk and the runner still found nothing:
// accepting the pass would ship tests nothing executed (the bootstrap_runner_not_evaluated class
// of bug). The evaluator turns the runner's pass back into a failure that names the files.
func TestRunEvaluation_noTestFiles_failsWhenGeneratedUnitTestsExist(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "src/app/router.test.tsx")
	runner := &stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true, Summary: "compile ok"},
		test:     StepResult{Step: StepTest, OK: true, Summary: "tests ok" + NoTestFilesSuffix, Output: "No test files found, exiting with code 1"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}
	opts := DefaultEvalOptions(dir, "typescript")
	opts.MaxFixIterations = 1
	opts.ArtifactPaths = []string{"src/app/router.test.tsx"}
	audit := &recordingAuditor{}
	res, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stable {
		t.Fatal("a generated unit test the runner never saw must not pass evaluation")
	}
	var testStep *StepResult
	for i := range res.StepResults {
		if res.StepResults[i].Step == StepTest {
			testStep = &res.StepResults[i]
		}
	}
	if testStep == nil || testStep.OK {
		t.Fatalf("test step must be reported failed; steps=%s", StepSummary(res.StepResults))
	}
	if !strings.Contains(testStep.Summary, "src/app/router.test.tsx") || !strings.Contains(testStep.Summary, "include pattern") {
		t.Errorf("summary must name the unseen file and the likely cause; got %q", testStep.Summary)
	}
	if !audit.hasStep("evaluator.no_test_files_with_generated_tests") {
		t.Error("the override must be audited as an error")
	}
}

// A test failure whose output names no writable file — here the bare "No test files found" a
// runner without the pass rule would report — must not reach the fixer at all, and must end the
// loop rather than re-run the suite for the rest of the budget. Run
// api-72dad6bb281cacee338f43c48432a780 sent exactly this output to the fixer three times; the
// third answer rewrote an unrelated Playwright spec.
func TestRunEvaluation_testFailureOutsideWritableScope_skipsFixerAndStops(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "e2e/routes/home.spec.tsx")
	writeArtifact(t, dir, "e2e/routes/announcements.spec.tsx")
	runner := &stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true, Summary: "compile ok"},
		test:     StepResult{Step: StepTest, OK: false, Summary: "tests failed", Output: "> vitest run\n\nNo test files found, exiting with code 1\ninclude: **/*.{test,spec}.{js,ts,tsx}\nexclude: **/node_modules/**, e2e/**\n"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{"e2e/routes/announcements.spec.tsx": "rewritten"}}}
	opts := DefaultEvalOptions(dir, "typescript")
	opts.MaxFixIterations = 5
	opts.Fixer = fixer
	opts.ArtifactPaths = []string{"e2e/routes/home.spec.tsx", "e2e/routes/announcements.spec.tsx"}
	audit := &recordingAuditor{}
	res, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stable {
		t.Fatal("the failing test step must leave the evaluation unstable")
	}
	if fixer.req.Step != "" || len(fixer.req.Files) != 0 {
		t.Fatalf("the fixer must not be invoked for a failure that names nothing writable; got request for step %q", fixer.req.Step)
	}
	if !audit.hasStep("evaluator.test_unrepairable_out_of_write_scope") {
		t.Error("the skipped fixer round must be audited")
	}
	if res.Iterations > 1 {
		t.Errorf("the loop must stop after the gate fires, not re-run the suite; iterations=%d", res.Iterations)
	}
	body, err := os.ReadFile(filepath.Join(dir, "e2e/routes/announcements.spec.tsx"))
	if err != nil || string(body) != "// generated\n" {
		t.Errorf("no file may be written when the fixer is skipped; got %q", string(body))
	}
}

// The gate must not fire when the failure DOES name a generated test: that is the ordinary repair
// path and the fixer must still be called.
func TestRunEvaluation_testFailureNamingArtifact_stillReachesFixer(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "src/lib/validation.test.ts")
	runner := &stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true, Summary: "compile ok"},
		test:     StepResult{Step: StepTest, OK: false, Summary: "tests failed", Output: " FAIL  src/lib/validation.test.ts > parsePositiveInt > decimals\nAssertionError: expected 3 to be null\n ❯ src/lib/validation.test.ts:27:38\n"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	opts := DefaultEvalOptions(dir, "typescript")
	opts.MaxFixIterations = 1
	opts.Fixer = fixer
	opts.ArtifactPaths = []string{"src/lib/validation.test.ts"}
	audit := &recordingAuditor{}
	if _, err := RunEvaluation(context.Background(), runner, opts, audit); err != nil {
		t.Fatal(err)
	}
	if fixer.req.Step != StepTest {
		t.Fatalf("a failure naming a generated test must reach the fixer; got step %q", fixer.req.Step)
	}
	if audit.hasStep("evaluator.test_unrepairable_out_of_write_scope") {
		t.Error("gate must not fire when a writable file is named")
	}
}
