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

// Upstream carries two more tests here, both on the targeted-edit response contract: that resolved
// edits land as whole-file writes, and that a round whose anchors ALL miss is reported as an
// unusable response rather than a silent no-op. They need FixResponse.Edits (CP51) and
// applyLLMFix's skip-reason return (CP52's breaker refactor), so they return with those bundles.
