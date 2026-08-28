package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePrimaryFailureSite(t *testing.T) {
	out := "[ERROR] COMPILATION ERROR : \n" +
		"[ERROR] /workspace/src/test/java/p/PetTests.java:[32,57] incompatible types\n" +
		"[ERROR] /workspace/src/test/java/p/VetControllerE2EIT.java:[57,17] no suitable method\n"
	got := ParsePrimaryFailureSite(out)
	if !got.OK || got.Line != 32 || !strings.HasSuffix(got.Path, "PetTests.java") {
		t.Fatalf("got %+v, want the FIRST diagnostic (PetTests.java:32)", got)
	}
	if bad := ParsePrimaryFailureSite("no locations here"); bad.OK {
		t.Errorf("expected no site, got %+v", bad)
	}
}

// The exact shape of the stall: the file is rewritten, the named line is not.
func TestTouchedPrimarySite(t *testing.T) {
	before := "package p;\nclass T {\n\tSet<Visit> a = pet.getVisits();\n\tint keep = 1;\n}\n"
	site := PrimaryFailureSite{Path: "src/test/java/p/T.java", Line: 3, OK: true}

	t.Run("rewrite that leaves the named line alone", func(t *testing.T) {
		after := strings.Replace(before, "int keep = 1;", "int keep = 2;", 1)
		touched, known := TouchedPrimarySite(site, "src/test/java/p/T.java", before, after)
		if !known {
			t.Fatal("this is answerable: same file, parseable site")
		}
		if touched {
			t.Error("changing a neighbouring line must not count as touching the blamed line")
		}
	})

	t.Run("edit at the named line", func(t *testing.T) {
		after := strings.Replace(before, "Set<Visit>", "Collection<Visit>", 1)
		touched, known := TouchedPrimarySite(site, "src/test/java/p/T.java", before, after)
		if !known || !touched {
			t.Errorf("touched=%v known=%v; the blamed line changed", touched, known)
		}
	})

	t.Run("identical content", func(t *testing.T) {
		if touched, known := TouchedPrimarySite(site, "src/test/java/p/T.java", before, before); touched || !known {
			t.Errorf("touched=%v known=%v", touched, known)
		}
	})

	t.Run("different file is unanswerable", func(t *testing.T) {
		if _, known := TouchedPrimarySite(site, "src/test/java/p/Other.java", before, "x"); known {
			t.Error("a write to another file says nothing about the primary site")
		}
	})

	t.Run("reformat alone is not a repair", func(t *testing.T) {
		after := strings.Replace(before, "\tSet<Visit> a", "        Set<Visit>  a", 1)
		if touched, _ := TouchedPrimarySite(site, "src/test/java/p/T.java", before, after); touched {
			t.Error("whitespace-only change must not read as touching the line")
		}
	})
}

// End to end: a round that rewrites the file but not the blamed line must say so.
func TestApplyLLMFix_reportsUntouchedPrimarySite(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/PetTests.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	before := "package p;\n\nclass PetTests {\n\t@Test void a() {\n\t\tSet<Visit> v = pet.getVisits();\n\t\tint keep = 1;\n\t}\n}\n"
	if err := os.WriteFile(full, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	// The model rewrites the file but leaves line 5 — the blamed line — alone.
	after := strings.Replace(before, "int keep = 1;", "int keep = 2;", 1)

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: after}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}
	errOut := "[ERROR] /workspace/" + rel + ":[5,20] incompatible types: Collection cannot be converted to Set\n"

	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, new(int), 3, nil, "")

	if !audit.hasStep("evaluator.fix_primary_site_untouched") {
		t.Error("a round that left the blamed line unchanged must report it; otherwise seven such rounds all look like repairs")
	}
}

// A round that DOES fix the blamed line must not be flagged.
func TestApplyLLMFix_noWarningWhenPrimarySiteFixed(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/PetTests.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	before := "package p;\n\nclass PetTests {\n\t@Test void a() {\n\t\tSet<Visit> v = pet.getVisits();\n\t}\n}\n"
	if err := os.WriteFile(full, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	after := strings.Replace(before, "Set<Visit>", "Collection<Visit>", 1)

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: after}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}
	errOut := "[ERROR] /workspace/" + rel + ":[5,20] incompatible types\n"

	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, new(int), 3, nil, "")

	if audit.hasStep("evaluator.fix_primary_site_untouched") {
		t.Error("the blamed line was repaired; no warning should be emitted")
	}
}

// The skip-reason half of upstream's remaining tests is restored here now that applyLLMFix reports
// one. A round the model wasted must be reported as RETRYABLE, not as an exhausted fixer: before
// the split, any no-write outcome was terminal, so a single unparseable response ended a run with a
// non-compiling tree.
//
// (Upstream's other two tests here ride on the targeted-edit response contract — that resolved
// edits land as whole-file writes, and that a round whose anchors all miss is reported rather than
// silently dropped. They need FixResponse.Edits, so they return with CP51.)
func TestApplyLLMFix_unusableReplyIsRetryableNotTerminal(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/PTest.java"
	writeRepoFile(t, repo, rel, "package p;\nclass P {\n  @Test void a() {}\n}\n")

	// The model answers with nothing usable.
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{}}}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	applied, _, reason := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[3,1] boom\n", &recordingAuditor{}, new(int), 3, nil, "")

	if applied {
		t.Fatal("nothing was written; this is not a fix")
	}
	if reason != FixSkipResponseUnusable {
		t.Fatalf("reason = %q, want %q so the caller can retry instead of ending the run", reason, FixSkipResponseUnusable)
	}

	// And the same reason must reach the caller through RunFix, flagged retryable.
	res := RunFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[3,1] boom\n", nil, 1, 3, nil)
	if res.SkippedReason != FixSkipResponseUnusable || !res.Retryable {
		t.Fatalf("RunFix reported reason=%q retryable=%v; a bad turn must stay distinguishable from an exhausted fixer",
			res.SkippedReason, res.Retryable)
	}
}

// A tripped breaker is the opposite case: terminal, and never retryable.
func TestRunFix_breakerTripIsTerminal(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/p/QTest.java"
	writeRepoFile(t, repo, rel, "package p;\nclass Q {\n  @Test void a() {}\n}\n")
	opts := EvalOptions{RepoPath: repo, Lang: "java",
		Fixer: &stubFixer{resp: FixResponse{Files: map[string]string{}}}, ArtifactPaths: []string{rel}}

	state := &FixLoopState{tripped: true, trippedReason: FixSkipLoopNoProgress}
	res := RunFix(context.Background(), opts, StepCompile, "[ERROR] boom\n", nil, 1, 3, state)
	if res.SkippedReason != FixSkipLoopNoProgress {
		t.Fatalf("reason = %q, want the breaker that fired", res.SkippedReason)
	}
	if res.Retryable {
		t.Error("an exhausted fixer must not be reported as retryable")
	}
}
