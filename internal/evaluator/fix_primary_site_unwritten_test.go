package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// primaryUnwrittenError blames OwnerControllerTests.java:427 first — the shape of every iteration in
// audit.log of 2026-08-29.
const primaryUnwrittenError = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/petclinic/OwnerControllerTests.java:[427,10] cannot find symbol
  symbol:   class AfterEach
  location: class petclinic.OwnerControllerTests
[ERROR] /workspace/src/test/java/petclinic/VetsTests.java:[29,21] cannot find symbol
  symbol:   method notNull(petclinic.Vets)
[ERROR] BUILD FAILURE`

// A round that writes a file OTHER than the one the compiler blames must say so.
//
// This was the blind spot: TouchedPrimarySite reports known=false for any path that is not the
// primary file, so the old guard (primarySiteKnown && !primarySiteTouched) could only fire when the
// model had already written the primary file. Three consecutive rounds that wrote only VetsTests.java
// while OwnerControllerTests.java:427 stayed blamed produced no event at all.
func TestApplyLLMFix_primarySiteNeverWritten_isAudited(t *testing.T) {
	dir := t.TempDir()
	primary := "src/test/java/petclinic/OwnerControllerTests.java"
	other := "src/test/java/petclinic/VetsTests.java"
	for _, rel := range []string{primary, other} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "package petclinic;\nimport org.junit.jupiter.api.Test;\nclass C { @Test void a() {} }\n"
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.ArtifactPaths = []string{primary, other}
	// The fixer answers with the non-blamed file only, which is what the run under post-mortem did.
	opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{
		other: "package petclinic;\nimport org.junit.jupiter.api.Test;\nclass C { @Test void a() {} @Test void b() {} }\n",
	}}}
	audit := &recordingAuditor{}
	attempts := 0
	var st FixLoopState

	applied, touched, _ := applyLLMFix(context.Background(), opts, StepCompile, primaryUnwrittenError, audit, &attempts, 5, &st, "")
	if !applied || len(touched) != 1 {
		t.Fatalf("applied=%v touched=%v; want the one non-primary write to land", applied, touched)
	}
	p := audit.lastPayload("evaluator.fix_primary_site_untouched")
	if p == nil {
		t.Fatal("no evaluator.fix_primary_site_untouched event; a round that never wrote the blamed file must be reported")
	}
	if written, _ := p["primary_path_written"].(bool); written {
		t.Errorf("primary_path_written = true; the round wrote only %s", other)
	}
	// javac cites the container path, so the recorded value keeps the /workspace prefix; the
	// suffix match in sameDiagnosticFile is what ties it to the repo-relative artifact.
	if got := p["primary_path"]; got != "workspace/src/test/java/petclinic/OwnerControllerTests.java" {
		t.Errorf("primary_path = %v; want the blamed file as the compiler named it", got)
	}
	if got := p["primary_line"]; got != 427 {
		t.Errorf("primary_line = %v; want 427", got)
	}
}

// The converse: a round that DOES repair the blamed line stays silent, so the event keeps meaning
// "this round did not address the primary diagnostic".
func TestApplyLLMFix_primarySiteRepaired_isNotAudited(t *testing.T) {
	dir := t.TempDir()
	primary := "src/test/java/petclinic/OwnerControllerTests.java"
	full := filepath.Join(dir, filepath.FromSlash(primary))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	// The blamed line (5) is the unimported annotation; the repair adds its import and drops the
	// annotation, so the blamed text no longer occurs.
	before := "package petclinic;\n" +
		"import org.junit.jupiter.api.Test;\n" +
		"class OwnerControllerTests {\n" +
		"	@Test void a() {}\n" +
		"	@AfterEach\n" +
		"	void tearDown() {}\n" +
		"}\n"
	if err := os.WriteFile(full, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	after := "package petclinic;\n" +
		"import org.junit.jupiter.api.AfterEach;\n" +
		"import org.junit.jupiter.api.Test;\n" +
		"class OwnerControllerTests {\n" +
		"	@Test void a() {}\n" +
		"	@AfterEach\n" +
		"	void tearDown() {}\n" +
		"}\n"
	errOut := "[ERROR] COMPILATION ERROR :\n" +
		"[ERROR] /workspace/src/test/java/petclinic/OwnerControllerTests.java:[5,10] cannot find symbol\n" +
		"  symbol:   class AfterEach\n" +
		"[ERROR] BUILD FAILURE"

	opts := DefaultEvalOptions(dir, "java")
	opts.ArtifactPaths = []string{primary}
	opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{primary: after}}}
	audit := &recordingAuditor{}
	attempts := 0
	var st FixLoopState

	applied, _, why := applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &attempts, 5, &st, "")
	if !applied {
		t.Fatalf("want the primary-file write to land; skip reason = %q", why)
	}
	if audit.hasStep("evaluator.fix_primary_site_untouched") {
		p := audit.lastPayload("evaluator.fix_primary_site_untouched")
		t.Errorf("a round that added the missing import on the blamed line must not be reported as leaving it untouched: %v", p)
	}
}
