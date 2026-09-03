package evaluator

import (
	"context"
	"testing"
)

// The run of 2026-09-01 is the reason compile narrowing exists on this path, and it is a TypeScript
// run: 18 artifacts went to the fixer for a tsc failure that named 12 files. Four of the six
// un-named ones had no error at all and two were Playwright specs that tsconfig.app.json does not
// compile. The reply had to reproduce all 18 in full — ~10.5k tokens against an 8192 cap — so the
// round could not have succeeded whatever the model wrote.
//
// This case needs BOTH halves of the work: narrowing must run on StepCompile, and
// errout.AllCitedRepoPaths must be able to see tsc's parenthesised `path(line,col)` positions. With
// either missing, every artifact stays writable.
func TestApplyLLMFix_narrowsTypeScriptCompileScope(t *testing.T) {
	repo := t.TempDir()
	cited := "src/app/AppLayout.test.tsx"
	writeRepoFile(t, repo, cited, "import { render } from '@testing-library/react';\n\ndescribe('AppLayout', () => {\n  it('renders', () => { render(null as never); });\n});\n")
	clean := "src/lib/validation.test.ts"
	writeRepoFile(t, repo, clean, "describe('validation', () => {\n  it('works', () => { expect(1).toBe(1); });\n});\n")
	e2e := "e2e/routes/home.spec.tsx"
	writeRepoFile(t, repo, e2e, "import { test } from '@playwright/test';\n\ntest('home', async () => {});\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "import { render } from '@testing-library/react';\n\ndescribe('AppLayout', () => {\n  it('renders', () => { render(<div />); });\n});\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath:      repo,
		Lang:          "typescript",
		Fixer:         fixer,
		ArtifactPaths: []string{cited, clean, e2e},
	}
	errOut := "\n> react-test@0.0.0 build\n> tsc --noEmit -p tsconfig.app.json && vite build\n\n" +
		"src/app/AppLayout.test.tsx(4,42): error TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'.\n"

	counter := 0
	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 3, nil, "")
	if len(fixer.req.ArtifactPaths) == 0 {
		t.Fatal("fixer was not invoked")
	}
	if len(fixer.req.ArtifactPaths) != 1 || normalizePathForFix(fixer.req.ArtifactPaths[0]) != normalizePathForFix(cited) {
		t.Fatalf("ArtifactPaths = %v, want only the cited %s", fixer.req.ArtifactPaths, cited)
	}
	// The uncited files must still reach the prompt as read-only context.
	for _, rel := range []string{clean, e2e} {
		if _, ok := fixer.req.Files[rel]; !ok {
			t.Errorf("uncited artifact %s vanished from the prompt; it must stay as read-only context", rel)
		}
	}
	if !audit.hasStep("evaluator.fix_scope_narrowed") {
		t.Error("expected evaluator.fix_scope_narrowed for a narrowed TypeScript compile round")
	}
	if p := audit.lastPayload("evaluator.fix_scope_narrowed"); p != nil {
		if p["reason"] != "error_cited_subset" {
			t.Errorf("reason = %v, want error_cited_subset", p["reason"])
		}
	}
}

// Compile narrows from attempt 1; only test steps defer. Compiler citations are exact, so there is
// no heuristic that a first-attempt narrowing could be wrong about.
func TestApplyLLMFix_compileNarrowsOnFirstAttempt(t *testing.T) {
	repo := t.TempDir()
	cited := "src/a.test.ts"
	writeRepoFile(t, repo, cited, "describe('a', () => { it('x', () => {}); });\n")
	other := "src/b.test.ts"
	writeRepoFile(t, repo, other, "describe('b', () => { it('y', () => {}); });\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "describe('a', () => { it('x', () => { const repaired = 1; void repaired; }); });\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{cited, other}}

	counter := 0 // first attempt
	applyLLMFix(context.Background(), opts, StepCompile, "src/a.test.ts(1,20): error TS1005: ',' expected.\n", audit, &counter, 3, nil, "")
	if len(fixer.req.ArtifactPaths) != 1 {
		t.Fatalf("ArtifactPaths = %v, want the compile round narrowed on attempt 1", fixer.req.ArtifactPaths)
	}
	if audit.hasStep("evaluator.fix_scope_narrowing_deferred") {
		t.Error("compile must not defer narrowing; that is a test-step rule")
	}
}

// A test step's first attempt ships the full page and records what it WOULD have narrowed to, so a
// first attempt without fix_scope_narrowed stays distinguishable from "nothing cited".
func TestApplyLLMFix_testStepDefersNarrowingOnFirstAttempt(t *testing.T) {
	repo := t.TempDir()
	cited := "src/a.test.ts"
	writeRepoFile(t, repo, cited, "describe('a', () => { it('x', () => {}); });\n")
	other := "src/b.test.ts"
	writeRepoFile(t, repo, other, "describe('b', () => { it('y', () => {}); });\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "describe('a', () => { it('x', () => { const repaired = 1; void repaired; }); });\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{cited, other}}

	counter := 0 // first attempt
	applyLLMFix(context.Background(), opts, StepTest, "src/a.test.ts(1,20): error TS1005: ',' expected.\n", audit, &counter, 3, nil, "")
	if len(fixer.req.ArtifactPaths) != 2 {
		t.Fatalf("ArtifactPaths = %v, want the full page on a test step's first attempt", fixer.req.ArtifactPaths)
	}
	if !audit.hasStep("evaluator.fix_scope_narrowing_deferred") {
		t.Fatal("expected evaluator.fix_scope_narrowing_deferred on a test step's first attempt")
	}
	p := audit.lastPayload("evaluator.fix_scope_narrowing_deferred")
	if p == nil {
		t.Fatal("no payload recorded")
	}
	if p["reason"] != "error_cited_subset" {
		t.Errorf("reason = %v, want error_cited_subset", p["reason"])
	}
	if got, ok := p["artifact_paths_would_scope"].([]string); !ok || len(got) != 1 {
		t.Errorf("artifact_paths_would_scope = %v, want the one-path subset it deferred", p["artifact_paths_would_scope"])
	}
}

// The failing-test fallback is new on this path. AllCitedRepoPaths only resolves compiler-shaped
// citations, and JUnit/surefire names a failing test by CLASS —
// `at com.example.VetsTests.badPort(VetsTests.java:40)` — a bare basename that resolves to no repo
// path. Without the fallback a test round that cited nothing resolvable stayed un-narrowed and the
// fixer was invited to rewrite healthy siblings alongside the failing one.
//
// Attempt 2, because a test step's first attempt ships the full page by design.
func TestApplyLLMFix_testStepNarrowsByFailingTestOnLaterAttempt(t *testing.T) {
	repo := t.TempDir()
	const failing = "src/test/java/com/example/VetsTests.java"
	const healthy = "src/test/java/com/example/OwnerTests.java"
	writeRepoFile(t, repo, failing, "package com.example;\nclass VetsTests {\n@Test void badPort() {}\n}\n")
	writeRepoFile(t, repo, healthy, "package com.example;\nclass OwnerTests {\n@Test void ok() {}\n}\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		failing: "package com.example;\nclass VetsTests {\n@Test void badPort() { int repaired = 1; }\n}\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{failing, healthy}}

	// Class-name-only failure output: no resolvable repo path anywhere in it.
	errOut := "[ERROR] Tests run: 1, Failures: 1\n" +
		"[ERROR] com.example.VetsTests.badPort:40 expected:<8080> but was:<0>\n" +
		"\tat com.example.VetsTests.badPort(VetsTests.java:40)\n"

	counter := 1 // -> currentAttempt == 2, past the first-attempt deferral
	applyLLMFix(context.Background(), opts, StepTest, errOut, audit, &counter, 3, nil, "")
	if len(fixer.req.ArtifactPaths) == 0 {
		t.Fatal("fixer was not invoked")
	}
	if len(fixer.req.ArtifactPaths) != 1 || normalizePathForFix(fixer.req.ArtifactPaths[0]) != normalizePathForFix(failing) {
		t.Fatalf("ArtifactPaths = %v, want only the failing %s", fixer.req.ArtifactPaths, failing)
	}
	if _, ok := fixer.req.Files[healthy]; !ok {
		t.Errorf("the healthy sibling must stay as read-only context")
	}
	if p := audit.lastPayload("evaluator.fix_scope_narrowed"); p == nil {
		t.Error("expected evaluator.fix_scope_narrowed")
	} else if p["reason"] != "failing_test_subset" {
		t.Errorf("reason = %v, want failing_test_subset", p["reason"])
	}
}

// F9. Narrowing to the cited file is right until the model shows it cannot progress there. The
// React run of 2026-09-03 narrowed to one file on attempt 2, got it back byte-identical twice, and
// stopped as "fixer unusable" with five other failing files never offered. After a stalled narrowed
// round the next round must offer every writable artifact once, then narrow again.
func TestApplyLLMFix_stalledNarrowedWriteWidensTheNextRound(t *testing.T) {
	repo := t.TempDir()
	cited := "src/a.test.ts"
	writeRepoFile(t, repo, cited, "describe(\x27a\x27, () => { it(\x27x\x27, () => { expect(1).toBe(2); }); });\n")
	other := "src/b.test.ts"
	writeRepoFile(t, repo, other, "describe(\x27b\x27, () => { it(\x27y\x27, () => { expect(1).toBe(1); }); });\n")

	// Round 1 writes a change to the narrowed file; the failure output of round 2 is identical,
	// so the write achieved nothing the compiler can see.
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		cited: "describe(\x27a\x27, () => { it(\x27x\x27, () => { const v = 1; expect(v).toBe(2); }); });\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "typescript", Fixer: fixer, ArtifactPaths: []string{cited, other}}
	errOut := "src/a.test.ts(1,40): error TS2345: Argument of type \x27number\x27 is not assignable.\n"
	ls := &FixLoopState{}
	counter := 0

	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 5, ls, "")
	if len(fixer.req.ArtifactPaths) != 1 {
		t.Fatalf("round 1 must be narrowed to the cited file; got %v", fixer.req.ArtifactPaths)
	}
	if audit.hasStep("evaluator.fix_scope_widened") {
		t.Fatal("nothing has stalled yet; round 1 must not widen")
	}

	// Round 2: same diagnostics for the file round 1 rewrote.
	fixer.resp = FixResponse{Files: map[string]string{}}
	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 5, ls, "")
	if !audit.hasStep("evaluator.fix_scope_widened") {
		t.Fatal("a narrowed round whose write changed nothing must widen the next round")
	}
	if p := audit.lastPayload("evaluator.fix_scope_widened"); p == nil || p["reason"] != "stalled_write" {
		t.Errorf("widen reason = %v, want stalled_write", p)
	}
	if len(fixer.req.ArtifactPaths) != 2 {
		t.Fatalf("the widened round must offer every writable artifact; got %v", fixer.req.ArtifactPaths)
	}

	// Round 3 narrows again from its own output: widening lasts one round. A DIFFERENT failure
	// this time, so the repeat breaker (three identical signatures) cannot return before the fixer
	// is asked and leave a stale request behind.
	errOut3 := "src/a.test.ts(2,10): error TS2304: Cannot find name 'expectt'.\n"
	applyLLMFix(context.Background(), opts, StepCompile, errOut3, audit, &counter, 5, ls, "")
	if len(fixer.req.ArtifactPaths) != 1 {
		t.Fatalf("narrowing must resume after the widened round; got %v", fixer.req.ArtifactPaths)
	}
	if n := len(audit.payloads["evaluator.fix_scope_widened"]); n != 1 {
		t.Errorf("widening must happen once per stall, got %d events", n)
	}
}
