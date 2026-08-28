package evaluator

import (
	"context"
	"strings"
	"testing"
)

// Item 1: narrowing must be computed from the PRE-narrowing writable set.
//
// Adoption widens that set (a failing test the run did not generate becomes writable); rebuilding
// the scope from opts.ArtifactPaths alone silently undid it, so the write gate permitted the
// adopted path while FixRequest.ArtifactPaths never told the model it could touch it — and the
// model therefore never returned it. Fails before the change: the adopted path is dropped.
func TestApplyLLMFix_narrowingKeepsAdoptedPaths(t *testing.T) {
	repo := t.TempDir()
	generated := "src/test/java/p/GeneratedTest.java"
	adopted := "src/test/java/p/InheritedTest.java"
	other := "src/test/java/p/UnrelatedTest.java"
	for _, rel := range []string{generated, adopted, other} {
		writeRepoFile(t, repo, rel, "package p;\nclass X {\n  @Test void a() {}\n}\n")
	}

	// The failure names the adopted file and the generated one, but not the third.
	errOut := "[ERROR] /workspace/" + adopted + ":[3,1] cannot find symbol\n" +
		"[ERROR] /workspace/" + generated + ":[3,1] cannot find symbol\n"

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath:                  repo,
		Lang:                      "java",
		Fixer:                     fixer,
		ArtifactPaths:             []string{generated, other},
		FailingTestCandidatePaths: []string{adopted},
	}

	applyLLMFix(context.Background(), opts, StepTest, errOut, audit, new(int), 3, nil, "")

	var sawAdopted bool
	for _, p := range fixer.req.ArtifactPaths {
		if normalizePathForFix(p) == normalizePathForFix(adopted) {
			sawAdopted = true
		}
	}
	if !sawAdopted {
		t.Fatalf("narrowing dropped the adopted path; the model is never told it may fix it: %v", fixer.req.ArtifactPaths)
	}
	// And the audit must report the true pre-narrowing basis, not just the generated set.
	if p := audit.lastPayload("evaluator.fix_scope_narrowed"); p != nil {
		all, _ := p["artifact_paths_all"].([]string)
		var inAll bool
		for _, x := range all {
			if normalizePathForFix(x) == normalizePathForFix(adopted) {
				inAll = true
			}
		}
		if !inAll {
			t.Errorf("artifact_paths_all understates the pre-narrowing set (adoption invisible): %v", all)
		}
	}
}

// Item 2's headline acceptance — a second round carries the prior attempt — is covered by the
// ported TestApplyLLMFix_secondRoundCarriesPriorAttempt in fix_attempt_memory_test.go, which is
// upstream's own test for it. What follows covers the half core's wiring adds on top: the early
// returns.

// Item 2, the other half: a round the model wasted must be banked too. "You answered and none of
// it was usable" is exactly as informative as a diff, and it is the case most likely to repeat.
func TestApplyLLMFix_unusableRoundIsStillBanked(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/BTest.java"
	writeRepoFile(t, repo, rel, "package p;\nclass B {\n  @Test void a() {}\n}\n")

	state := &FixLoopState{}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}} // nothing to apply
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[3,1] boom\n", &recordingAuditor{}, new(int), 3, state, "")

	if len(state.attempts) == 0 {
		t.Fatal("a round that produced nothing usable left no record; the next prompt cannot say so")
	}
	if !strings.Contains(RenderFixAttemptMemory(state.attempts), "no file") {
		t.Errorf("the note must tell the model what went wrong: %q", RenderFixAttemptMemory(state.attempts))
	}
}

// Item 3: a coverage-deleting fix is rejected. The failure mode is the fixer resolving a compile
// error by deleting the tests and the round being recorded as a success.
func TestApplyLLMFix_rejectsACoverageDeletingFix(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/CTest.java"
	before := "package p;\nclass C {\n  @Test void a() { x(); }\n  @Test void b() { y(); }\n  @Test void c() { z(); }\n}\n"
	writeRepoFile(t, repo, rel, before)
	// "Fixes" the build by deleting two of the three tests.
	gutted := "package p;\nclass C {\n  @Test void a() { x(); }\n}\n"

	audit := &recordingAuditor{}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: gutted}}}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[4,1] cannot find symbol\n", audit, new(int), 3, nil, "")

	if applied {
		t.Fatal("a fix that deletes tests must not be applied")
	}
	if !audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("the rejection must be visible in the audit")
	}
	// The escape hatch exists, and using it is loud.
	audit2 := &recordingAuditor{}
	opts.AllowFixCoverageReduction = true
	if applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[4,1] cannot find symbol\n", audit2, new(int), 3, nil, ""); !applied {
		t.Fatal("the escape hatch must let a deliberate reduction through")
	}
	if !audit2.hasStep("evaluator.fix_coverage_regression_allowed") {
		t.Error("every use of the escape hatch must be audited")
	}
}

// Item 4: an inherited failure is classified as inherited and reaches the fixer as a writable path
// on evidence — not on a regex guess over the current diagnostic.
func TestApplyLLMFix_adoptsBaselineFailureStillImplicated(t *testing.T) {
	repo := t.TempDir()
	generated := "src/test/java/p/GenTest.java"
	inherited := "src/test/java/p/OldTest.java"
	writeRepoFile(t, repo, generated, "package p;\nclass G {\n  @Test void a() {}\n}\n")
	writeRepoFile(t, repo, inherited, "package p;\nclass O {\n  @Test void a() {}\n}\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath:             repo,
		Lang:                 "java",
		Fixer:                fixer,
		ArtifactPaths:        []string{generated},
		BaselineFailingPaths: []string{inherited},
	}

	errOut := "[ERROR] /workspace/" + inherited + ":[3,1] cannot find symbol\n"
	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, new(int), 3, nil, "")

	var sawInherited bool
	for _, p := range fixer.req.ArtifactPaths {
		if normalizePathForFix(p) == normalizePathForFix(inherited) {
			sawInherited = true
		}
	}
	if !sawInherited {
		t.Fatalf("an inherited failure the diagnostic still names must be writable: %v", fixer.req.ArtifactPaths)
	}
	if !audit.hasStep("evaluator.fix_baseline_adopted") {
		t.Error("adoption on baseline evidence must be auditable")
	}
}

// The other half of item 4: a baseline failure the CURRENT diagnostic does not name is not this
// round's problem, and must not widen scope. Without this the budget goes on something nothing
// is asking about.
func TestApplyLLMFix_doesNotAdoptUnimplicatedBaselineFailure(t *testing.T) {
	repo := t.TempDir()
	generated := "src/test/java/p/GenTest.java"
	inherited := "src/test/java/p/OldTest.java"
	writeRepoFile(t, repo, generated, "package p;\nclass G {\n  @Test void a() {}\n}\n")
	writeRepoFile(t, repo, inherited, "package p;\nclass O {\n  @Test void a() {}\n}\n")

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	opts := EvalOptions{
		RepoPath:             repo,
		Lang:                 "java",
		Fixer:                fixer,
		ArtifactPaths:        []string{generated},
		BaselineFailingPaths: []string{inherited},
	}

	errOut := "[ERROR] /workspace/" + generated + ":[3,1] cannot find symbol\n"
	applyLLMFix(context.Background(), opts, StepCompile, errOut, &recordingAuditor{}, new(int), 3, nil, "")

	for _, p := range fixer.req.ArtifactPaths {
		if normalizePathForFix(p) == normalizePathForFix(inherited) {
			t.Fatal("a baseline failure this round's diagnostic never names must stay out of scope")
		}
	}
}
