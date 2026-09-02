package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compile round used to ship every artifact as writable. With 9 large Java test files that meant
// asking the model to reproduce ~334k runes of source, which hit the provider's 16k output cap and
// lost the round entirely. Narrowing to the artifacts the compiler actually named keeps the reply
// within reach.
func TestApplyLLMFix_narrowsCompileScopeToCitedArtifacts(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) string {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return rel
	}
	broken := write("src/test/java/p/OwnerControllerTests.java", "package p;\nclass OwnerControllerTests {\n@Test void a() {}\n}\n")
	healthy := write("src/test/java/p/VetControllerTests.java", "package p;\nclass VetControllerTests {\n@Test void b() {}\n}\n")
	other := write("src/test/java/p/PetTest.java", "package p;\nclass PetTest {\n@Test void c() {}\n}\n")

	// The reply must actually change the blamed line (3). Returning the on-disk content verbatim
	// is the regurgitation case the primary-site enforcement rejects, which is not what this test
	// is about.
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		broken: "package p;\nclass OwnerControllerTests {\n@Test void a() { int repaired = 1; }\n}\n",
	}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath:      repo,
		Lang:          "java",
		Fixer:         fixer,
		ArtifactPaths: []string{broken, healthy, other},
	}
	errOut := "[ERROR] " + filepath.Join(repo, filepath.FromSlash(broken)) + ":[3,9] class, interface, enum, or record expected\n"

	counter := 0
	applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 3, nil, "")
	if !applied {
		t.Fatalf("expected the fix to apply")
	}
	if len(fixer.req.ArtifactPaths) == 0 {
		t.Fatal("fixer was not invoked")
	}

	// Only the cited artifact may be rewritten.
	if len(fixer.req.ArtifactPaths) != 1 || normalizePathForFix(fixer.req.ArtifactPaths[0]) != normalizePathForFix(broken) {
		t.Fatalf("ArtifactPaths = %v, want only %s", fixer.req.ArtifactPaths, broken)
	}
	// …but the others must remain visible as read-only context, or the fixer loses the API surface
	// it needs to reason about shared helpers.
	for _, rel := range []string{healthy, other} {
		if _, ok := fixer.req.Files[rel]; !ok {
			t.Errorf("non-cited artifact %s was dropped from the prompt entirely; it must stay as read-only context", rel)
		}
	}
	if !audit.hasStep("evaluator.fix_scope_narrowed") {
		t.Error("expected evaluator.fix_scope_narrowed audit event for a narrowed compile round")
	}
}

// When the compiler names every artifact there is nothing to narrow, and the full set stays writable.
func TestApplyLLMFix_compileScopeNotNarrowedWhenAllArtifactsCited(t *testing.T) {
	repo := t.TempDir()
	var rels []string
	var errOut strings.Builder
	errOut.WriteString("[ERROR] COMPILATION ERROR :\n")
	for _, name := range []string{"ATest", "BTest"} {
		rel := "src/test/java/p/" + name + ".java"
		full := filepath.Join(repo, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("package p;\nclass "+name+" {\n@Test void x() {}\n}\n"), 0o644)
		rels = append(rels, rel)
		errOut.WriteString("[ERROR] " + filepath.Join(repo, filepath.FromSlash(rel)) + ":[3,9] boom\n")
	}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{
		rels[0]: "package p;\nclass ATest {\n@Test void x() {}\n}\n",
	}}}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: rels}

	counter := 0
	applyLLMFix(context.Background(), opts, StepCompile, errOut.String(), &recordingAuditor{}, &counter, 3, nil, "")
	if len(fixer.req.ArtifactPaths) == 0 {
		t.Fatal("fixer was not invoked")
	}
	if len(fixer.req.ArtifactPaths) != len(rels) {
		t.Fatalf("ArtifactPaths = %v, want all %d artifacts writable when all are cited", fixer.req.ArtifactPaths, len(rels))
	}
}
