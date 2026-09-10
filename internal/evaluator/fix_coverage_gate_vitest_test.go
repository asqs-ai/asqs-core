package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Verbatim shape from run an asqs-go React run (vitest 4.1.11). Two suites failed
// to COLLECT — AppLayout.test.tsx because the generator opened it with its own path, which vitest
// reports as `ReferenceError: src is not defined` — and vitest scores a suite it could not collect
// as `(0 test)`. Two other suites ran and failed; one ran and passed.
const vitestZeroTestOutput = `
> react-test@0.0.0 test
> vitest run


 RUN  v4.1.11 /workspace

 ❯ src/app/router.test.tsx (0 test)
 ❯ src/pages/PaymentPlaygroundPage.test.tsx (4 tests | 4 failed) 95ms
     × should render the payment playground interface 22ms
 ❯ src/app/AppLayout.test.tsx (0 test)
 ❯ src/pages/OrdersPage.test.tsx (5 tests | 4 failed) 3115ms
     × should display a loading state when fetching orders 7ms
 ✓ src/pages/HomePage.test.tsx (3 tests) 99ms

⎯⎯⎯⎯⎯⎯ Failed Suites 2 ⎯⎯⎯⎯⎯⎯⎯

 FAIL  src/app/AppLayout.test.tsx [ src/app/AppLayout.test.tsx ]
ReferenceError: src is not defined
 ❯ src/app/AppLayout.test.tsx:1:1
      1| src/app/AppLayout.test.tsx
       | ^
      2| import { render, screen } from '@testing-library/react';
`

// testFilesWithNoRunnableTests understood only jest's "Your test suite must contain at least one
// test" block, which vitest never prints — so on a vitest project the waiver could not fire at
// all, and the map came back empty.
func TestTestFilesWithNoRunnableTests_readsVitestZeroTestFiles(t *testing.T) {
	got := testFilesWithNoRunnableTests(vitestZeroTestOutput)
	for _, want := range []string{"src/app/router.test.tsx", "src/app/AppLayout.test.tsx"} {
		if !got[want] {
			t.Errorf("%s ran zero tests and should be reported; got %v", want, got)
		}
	}
	for _, ran := range []string{
		"src/pages/PaymentPlaygroundPage.test.tsx",
		"src/pages/OrdersPage.test.tsx",
		"src/pages/HomePage.test.tsx",
	} {
		if got[ran] {
			t.Errorf("%s executed tests and must not be reported; got %v", ran, got)
		}
	}
}

// vitest pluralises the count and colours the line; neither changes the fact it reports.
func TestTestFilesWithNoRunnableTests_vitestSurfaceVariants(t *testing.T) {
	cases := []struct{ name, line string }{
		{"singular", " ❯ src/a.test.ts (0 test)"},
		{"plural", " ❯ src/a.test.ts (0 tests)"},
		{"coloured", " \x1b[31m❯\x1b[39m src/a.test.ts \x1b[2m(\x1b[22m\x1b[2m0 test\x1b[22m\x1b[2m)\x1b[22m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testFilesWithNoRunnableTests(tc.line + "\n"); !got["src/a.test.ts"] {
				t.Fatalf("got %v, want src/a.test.ts", got)
			}
		})
	}
	// A suite that ran is never reported, however many of its tests failed.
	if got := testFilesWithNoRunnableTests(" ❯ src/a.test.ts (10 tests | 10 failed) 40ms\n"); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

// The end-to-end consequence, and the whole point of the fix. AppLayout.test.tsx statically
// declares 6 `it(` blocks and vitest executed none of them, so the fixer's 5-test rewrite is the
// repair, not a regression. Without the waiver the gate rejected it identically on six
// consecutive rounds — 16:56 to 18:27, 91 minutes — and the file was discarded unexamined.
func TestApplyLLMFix_coverageGateWaivedForVitestZeroTestSuite(t *testing.T) {
	repo := t.TempDir()
	const artifact = "src/app/AppLayout.test.tsx"

	var before strings.Builder
	before.WriteString("import { render, screen } from '@testing-library/react';\n\ndescribe('AppLayout', () => {\n")
	for i := 0; i < 6; i++ {
		before.WriteString("  it('case " + string(rune('a'+i)) + "', () => {});\n")
	}
	before.WriteString("});\n")
	writeRepoFile(t, repo, artifact, before.String())

	rewritten := "import { render, screen } from '@testing-library/react';\n\ndescribe('AppLayout', () => {\n" +
		"  it('renders the shell', () => { expect(1).toBe(1); });\n" +
		"  it('renders the nav', () => { expect(1).toBe(1); });\n" +
		"  it('renders the outlet', () => { expect(1).toBe(1); });\n" +
		"  it('applies the layout class', () => { expect(1).toBe(1); });\n" +
		"  it('renders the footer', () => { expect(1).toBe(1); });\n" +
		"});\n"

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{artifact: rewritten}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{artifact}}

	counter := 0
	applied, touched, reason := applyLLMFix(context.Background(), opts, StepTest, vitestZeroTestOutput, audit, &counter, 5, nil, "")
	if !applied || len(touched) != 1 {
		t.Fatalf("expected the rewrite to be applied (applied=%v touched=%v reason=%q)", applied, touched, reason)
	}
	if audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("the gate must not defend a test count the runner never executed")
	}
	if !audit.hasStep("evaluator.fix_coverage_gate_waived") {
		t.Error("expected evaluator.fix_coverage_gate_waived to record why the gate stood down")
	}
	disk, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(artifact)))
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != rewritten {
		t.Error("the rewrite did not land on disk")
	}
}

// The waiver stays per file: a vitest suite that DID run keeps the gate, so the fixer still cannot
// answer four failures by deleting a test.
func TestApplyLLMFix_coverageGateStillRejectsForVitestSuitesThatRan(t *testing.T) {
	repo := t.TempDir()
	const artifact = "src/pages/OrdersPage.test.tsx"
	writeRepoFile(t, repo, artifact, "describe('OrdersPage', () => {\n  it('a', () => {});\n  it('b', () => {});\n});\n")
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		artifact: "describe('OrdersPage', () => {\n  it('a', () => { expect(1).toBe(1); });\n});\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{artifact}}

	counter := 0
	if applied, _, _ := applyLLMFix(context.Background(), opts, StepTest, vitestZeroTestOutput, audit, &counter, 5, nil, ""); applied {
		t.Fatal("a 2 → 1 rewrite of a suite that ran must still be rejected")
	}
	if !audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("expected evaluator.fix_rejected_coverage_regression")
	}
}
