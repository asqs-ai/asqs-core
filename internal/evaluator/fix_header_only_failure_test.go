package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jest output shape from the asqs-core audit.log of 2026-09-03: one suite failed to run with no
// source location at all (only its FAIL header), one failed with a located assertion, one passed.
const jestHeaderOnlyFailureOutput = `
> angular-test@0.0.0 test:asqs
> jest

FAIL src/app/features/checkout/checkout.component.test.ts
  ● Test suite failed to run

    Your test suite must contain at least one test.

      at onResult (node_modules/@jest/core/build/TestScheduler.js:133:18)

PASS src/app/features/checkout/pricing.service.test.ts
FAIL src/app/legacy/services/legacy-invoice-bridge.service.test.ts
  ● LegacyInvoiceBridgeService › should be created

    Property ` + "`constructor`" + ` does not have access type get

      at ModuleMocker._spyOnProperty (node_modules/jest-mock/build/index.js:819:13)
      at src/app/legacy/services/legacy-invoice-bridge.service.test.ts:18:10
`

func TestTestFilesWithNoRunnableTests_readsJestHeaderBlocks(t *testing.T) {
	got := testFilesWithNoRunnableTests(jestHeaderOnlyFailureOutput)
	if !got["src/app/features/checkout/checkout.component.test.ts"] {
		t.Fatalf("checkout.component.test.ts should be reported as having no runnable test; got %v", got)
	}
	if got["src/app/legacy/services/legacy-invoice-bridge.service.test.ts"] || got["src/app/features/checkout/pricing.service.test.ts"] {
		t.Fatalf("only the empty-suite file may be reported; got %v", got)
	}
	// Timing suffix on the header, and a PASS header ending the block.
	out := "FAIL src/x.test.ts (1.2 s)\n  ● Test suite failed to run\n\n    Your test suite must contain at least one test.\nPASS src/y.test.ts\n    Your test suite must contain at least one test.\n"
	got = testFilesWithNoRunnableTests(out)
	if !got["src/x.test.ts"] || len(got) != 1 {
		t.Fatalf("got %v, want only src/x.test.ts", got)
	}
}

// Attempt 2 of a test step: the cited-subset narrowing used to keep ONLY files with a `path:line`
// citation. The suite jest refused to run has no such citation, so it left the writable scope for
// the rest of the loop and was eventually discarded unexamined.
func TestApplyLLMFix_headerOnlyFailingSuiteStaysInScope(t *testing.T) {
	repo := t.TempDir()
	const empty = "src/app/features/checkout/checkout.component.test.ts"
	const cited = "src/app/legacy/services/legacy-invoice-bridge.service.test.ts"
	const passing = "src/app/features/checkout/pricing.service.test.ts"
	writeRepoFile(t, repo, empty, "import { CheckoutComponent } from './checkout.component';\n\ndescribe('CheckoutComponent', () => {\n  it('a', () => {});\n});\n")
	writeRepoFile(t, repo, cited, "describe('LegacyInvoiceBridgeService', () => {\n  it('should be created', () => {});\n});\n")
	writeRepoFile(t, repo, passing, "describe('PricingService', () => {\n  it('ok', () => { expect(1).toBe(1); });\n});\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "describe('LegacyInvoiceBridgeService', () => {\n  it('should be created', () => { const repaired = 1; void repaired; });\n});\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{empty, cited, passing}}

	counter := 1 // -> currentAttempt == 2, past the first-attempt deferral
	applyLLMFix(context.Background(), opts, StepTest, jestHeaderOnlyFailureOutput, audit, &counter, 5, nil, "")
	if len(fixer.req.ArtifactPaths) == 0 {
		t.Fatal("fixer was not invoked")
	}
	want := map[string]bool{normalizePathForFix(empty): true, normalizePathForFix(cited): true}
	got := map[string]bool{}
	for _, p := range fixer.req.ArtifactPaths {
		got[normalizePathForFix(p)] = true
	}
	if len(got) != 2 || !got[normalizePathForFix(empty)] || !got[normalizePathForFix(cited)] {
		t.Fatalf("ArtifactPaths = %v, want exactly the two failing files %v", fixer.req.ArtifactPaths, want)
	}
	if p := audit.lastPayload("evaluator.fix_scope_narrowed"); p == nil {
		t.Error("expected evaluator.fix_scope_narrowed (3 → 2)")
	} else if p["reason"] != "error_cited_subset" {
		t.Errorf("reason = %v, want error_cited_subset (cited-subset keeps priority; the header-only file is unioned in)", p["reason"])
	}
}

// The coverage gate counts `it(` statically. For a suite the runner refused to run that count was
// never executed, so a rewrite with fewer tests is not a regression; in the run of 2026-09-03 the
// gate rejected the only round in which checkout.component.test.ts was writable (6 → 5).
func TestApplyLLMFix_coverageGateWaivedForSuiteWithNoRunnableTests(t *testing.T) {
	repo := t.TempDir()
	const empty = "src/app/features/checkout/checkout.component.test.ts"
	const cited = "src/app/legacy/services/legacy-invoice-bridge.service.test.ts"
	// Six `it(` calls that jest never registered.
	var before strings.Builder
	before.WriteString("import { test } from 'node:test';\n\ndescribe('CheckoutComponent', () => {\n")
	for i := 0; i < 6; i++ {
		before.WriteString("  it('case', () => {});\n")
	}
	before.WriteString("});\n")
	writeRepoFile(t, repo, empty, before.String())
	writeRepoFile(t, repo, cited, "describe('LegacyInvoiceBridgeService', () => {\n  it('should be created', () => {});\n});\n")

	rewritten := "describe('CheckoutComponent', () => {\n" +
		"  it('computes a line total', () => { expect(2 * 3).toBe(6); });\n" +
		"  it('handles zero', () => { expect(0 * 3).toBe(0); });\n" +
		"  it('handles one', () => { expect(1 * 3).toBe(3); });\n" +
		"  it('handles many', () => { expect(4 * 3).toBe(12); });\n" +
		"  it('handles negatives', () => { expect(-1 * 3).toBe(-3); });\n" +
		"});\n"
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{empty: rewritten}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{empty, cited}}

	counter := 0
	applied, touched, reason := applyLLMFix(context.Background(), opts, StepTest, jestHeaderOnlyFailureOutput, audit, &counter, 5, nil, "")
	if !applied {
		t.Fatalf("expected the rewrite to be applied (reason=%q)", reason)
	}
	if len(touched) != 1 || normalizePathForFix(touched[0]) != normalizePathForFix(empty) {
		t.Fatalf("touched = %v, want %s", touched, empty)
	}
	if audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("the coverage gate must not reject a rewrite of a suite the runner refused to run")
	}
	if !audit.hasStep("evaluator.fix_coverage_gate_waived") {
		t.Error("expected evaluator.fix_coverage_gate_waived to record why the gate stood down")
	}
	disk, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(empty)))
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != rewritten {
		t.Error("the rewrite did not land on disk")
	}
}

// The waiver is per file: a suite that DID run keeps the gate.
func TestApplyLLMFix_coverageGateStillRejectsForSuitesThatRan(t *testing.T) {
	repo := t.TempDir()
	const cited = "src/app/legacy/services/legacy-invoice-bridge.service.test.ts"
	writeRepoFile(t, repo, cited, "describe('LegacyInvoiceBridgeService', () => {\n  it('a', () => {});\n  it('b', () => {});\n});\n")
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "describe('LegacyInvoiceBridgeService', () => {\n  it('a', () => { expect(1).toBe(1); });\n});\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{cited}}
	counter := 0
	applied, _, _ := applyLLMFix(context.Background(), opts, StepTest, jestHeaderOnlyFailureOutput, audit, &counter, 5, nil, "")
	if applied {
		t.Fatal("a 2 → 1 rewrite of a suite that ran must still be rejected")
	}
	if !audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("expected evaluator.fix_rejected_coverage_regression")
	}
}
