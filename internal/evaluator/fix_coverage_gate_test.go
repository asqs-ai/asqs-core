package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real before/after from run api-d7e0cbece3e9260f73836f5d50d21c96. The fixer resolved the
// compile error by deleting the generated Playwright tests and leaving a comment where they had
// been — and the round was recorded as a successful fix.
const welcomeBefore = `package org.springframework.samples.petclinic.system;

import com.microsoft.playwright.*;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class WelcomeControllerTests {

	private Playwright playwright;

	@BeforeEach
	void setUp() {
		playwright = Playwright.create();
	}

	@AfterEach
	void tearDown() {
		playwright.close();
	}

	@Test
	void welcomePageLoads() {
		assertThat(true).isTrue();
	}

	@Test
	void welcomePageShowsTitle() {
		assertThat(true).isTrue();
	}

}
`

const welcomeAfterDeletion = `package org.springframework.samples.petclinic.system;

import com.microsoft.playwright.*;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class WelcomeControllerTests {

	@Test
	void welcomePageLoads() {
		assertThat(true).isTrue();
	}

	// This class is not meant to be used for E2E tests with Playwright,
	// so we'll skip the Playwright-based test methods here.

}
`

func javaArtifactRepo(t *testing.T, body string) (repo, rel string) {
	t.Helper()
	repo = t.TempDir()
	rel = "src/test/java/org/springframework/samples/petclinic/system/WelcomeControllerTest.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, rel
}

func TestApplyLLMFix_rejectsCoverageDeletingFix(t *testing.T) {
	repo, rel := javaArtifactRepo(t, welcomeBefore)
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: welcomeAfterDeletion}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	applied, touched := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[10,17] cannot find symbol\n", audit, new(int), 3, nil, "")

	if applied || len(touched) != 0 {
		t.Errorf("a fix that deletes tests must not be applied (applied=%v touched=%v)", applied, touched)
	}
	onDisk, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != welcomeBefore {
		t.Errorf("file on disk was modified despite rejection:\n%s", onDisk)
	}
	if !audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("rejection must be audited")
	}
}

// The escape hatch exists for the repo where a test genuinely has to go, and its use must be loud.
func TestApplyLLMFix_coverageReductionAllowedByConfig(t *testing.T) {
	repo, rel := javaArtifactRepo(t, welcomeBefore)
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: welcomeAfterDeletion}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel},
		AllowFixCoverageReduction: true,
	}

	applied, _ := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[10,17] cannot find symbol\n", audit, new(int), 3, nil, "")

	if !applied {
		t.Error("with the escape hatch set the fix must apply")
	}
	if !audit.hasStep("evaluator.fix_coverage_regression_allowed") {
		t.Error("using the escape hatch must be audited")
	}
}

// A genuine repair keeps the tests and must pass untouched. This is the case the gate must not break.
func TestApplyLLMFix_acceptsRepairThatKeepsCoverage(t *testing.T) {
	repo, rel := javaArtifactRepo(t, welcomeBefore)
	// Same two tests, renamed and repaired — exactly what a correct fix looks like.
	repaired := strings.ReplaceAll(welcomeBefore, "welcomePageLoads", "welcomePageLoadsOk")
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: repaired}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	// Line 23 is `void welcomePageLoads() {`, the line the rename actually changes. The diagnostic
	// must name a line the repair touches, or the primary-site enforcement correctly rejects the
	// round as a rewrite that left the blamed line alone.
	applied, _ := applyLLMFix(context.Background(), opts, StepCompile,
		"[ERROR] /workspace/"+rel+":[23,7] cannot find symbol\n", audit, new(int), 3, nil, "")

	if !applied {
		t.Fatal("a rename-and-repair must be applied")
	}
	if audit.hasStep("evaluator.fix_rejected_coverage_regression") {
		t.Error("renaming a test is not a coverage regression")
	}
}

func TestCoverageRegressionReason(t *testing.T) {
	cases := []struct {
		name, path, before, after string
		wantReject                bool
	}{
		{"java drop", "A.java", "@Test void a(){}\n@Test void b(){}", "@Test void a(){}", true},
		{"java same", "A.java", "@Test void a(){}", "@Test void aRenamed(){}", false},
		{"java grow", "A.java", "@Test void a(){}", "@Test void a(){}\n@Test void b(){}", false},
		{"csharp drop", "A.cs", "[Fact] void a(){}\n[Theory] void b(){}", "[Fact] void a(){}", true},
		{"ts drop", "a.test.ts", "it('a',()=>{});\ntest('b',()=>{});", "it('a',()=>{});", true},
		{"go drop", "a_test.go", "func TestA(t *testing.T){}\nfunc TestB(t *testing.T){}", "func TestA(t *testing.T){}", true},
		{"unknown lang", "a.rb", "def test_a; end\ndef test_b; end", "def test_a; end", false},
		{"fresh file", "A.java", "", "@Test void a(){}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coverageRegressionReason(tc.path, tc.before, tc.after)
			if (got != "") != tc.wantReject {
				t.Errorf("reason = %q, wantReject = %v", got, tc.wantReject)
			}
		})
	}
}

// The residue check must blame only imports THIS write added, not ones that were already unused.
func TestUnusedImportResidueReason(t *testing.T) {
	before := "package p;\n\nimport java.util.Set;\n\nclass T { Set<String> s; }\n"
	after := "package p;\n\nimport java.util.Set;\nimport com.microsoft.playwright.Page;\n\nclass T { Set<String> s; }\n"
	got := unusedImportResidueReason("T.java", before, after)
	if !strings.Contains(got, "Page") {
		t.Errorf("newly added unused import not reported: %q", got)
	}
	if strings.Contains(got, "Set") {
		t.Errorf("pre-existing and used import wrongly blamed: %q", got)
	}
	if r := unusedImportResidueReason("T.java", before, before); r != "" {
		t.Errorf("no-op write must report nothing, got %q", r)
	}
}
