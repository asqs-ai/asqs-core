package evaluator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
)

type stubAPIProvider struct {
	surfaces []apisurface.TypeSurface
	err      error
	gotArgs  []apisurface.Target
}

func (s *stubAPIProvider) Lookup(_ context.Context, _ string, targets []apisurface.Target) ([]apisurface.TypeSurface, error) {
	s.gotArgs = targets
	return s.surfaces, s.err
}

func javaRepoWithTest(t *testing.T) (repo, rel string) {
	t.Helper()
	repo = t.TempDir()
	rel = "src/test/java/org/springframework/samples/petclinic/owner/OwnerControllerTest.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package p;\nclass OwnerControllerTest {\n@Test void a() {}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, rel
}

// The whole point of F02: the member list the fixer could never obtain must reach FixRequest.
func TestApplyLLMFix_attachesAPISurface(t *testing.T) {
	repo, rel := javaRepoWithTest(t)
	provider := &stubAPIProvider{surfaces: []apisurface.TypeSurface{{
		FQCN:    "com.microsoft.playwright.assertions.PageAssertions",
		Members: []string{"public default void hasURL(java.lang.String);"},
		Origin:  "playwright-1.49.0.jar",
	}}}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: "package p;\nclass OwnerControllerTest {\n@Test void a() { int fixed = 1; }\n}\n"}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath: repo, Lang: "java", Fixer: fixer,
		ArtifactPaths:      []string{rel},
		APISurfaceProvider: provider,
	}
	errOut := "[ERROR] /workspace/" + rel + ":[122,33] cannot find symbol\n" +
		"  symbol:   method hasURLContaining(java.lang.String)\n" +
		"  location: interface com.microsoft.playwright.assertions.PageAssertions\n"

	counter := 0
	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 3, nil, "")

	if len(fixer.req.APISurface) != 1 {
		t.Fatalf("FixRequest.APISurface = %+v; the fixer is still guessing at the member list", fixer.req.APISurface)
	}
	if !strings.Contains(fixer.req.APISurface[0].Members[0], "hasURL(") {
		t.Errorf("surface does not carry the real member: %+v", fixer.req.APISurface[0])
	}
	if len(provider.gotArgs) == 0 || provider.gotArgs[0].Member != "hasURLContaining" {
		t.Errorf("provider was not told which member was rejected: %+v", provider.gotArgs)
	}
	if !audit.hasStep("evaluator.fix_api_surface") {
		t.Error("resolution must be audited")
	}
}

// A provider that cannot resolve must never fail the round — it degrades to no block, audited.
func TestApplyLLMFix_apiSurfaceFailureIsNonFatal(t *testing.T) {
	repo, rel := javaRepoWithTest(t)
	provider := &stubAPIProvider{err: errors.New("dependency:build-classpath failed")}
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: "package p;\nclass OwnerControllerTest {\n@Test void a() { int fixed = 1; }\n}\n"}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{
		RepoPath: repo, Lang: "java", Fixer: fixer,
		ArtifactPaths:      []string{rel},
		APISurfaceProvider: provider,
	}
	errOut := "[ERROR] /workspace/" + rel + ":[122,33] cannot find symbol\n" +
		"  symbol:   method hasURLContaining(java.lang.String)\n" +
		"  location: interface com.microsoft.playwright.assertions.PageAssertions\n"

	counter := 0
	applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, &counter, 3, nil, "")

	if !applied {
		t.Error("a classpath failure must not stop the fixer from running")
	}
	if len(fixer.req.APISurface) != 0 {
		t.Errorf("expected no surface on failure, got %+v", fixer.req.APISurface)
	}
	if !audit.hasStep("evaluator.fix_api_surface_unavailable") {
		t.Error("degradation must be audited, not silent")
	}
}

// No provider configured is the pre-F02 behaviour and must stay clean: no block, no audit noise.
func TestApplyLLMFix_noProviderNoSurface(t *testing.T) {
	repo, rel := javaRepoWithTest(t)
	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: "package p;\nclass OwnerControllerTest {\n@Test void a() { int fixed = 1; }\n}\n"}}}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}

	counter := 0
	applyLLMFix(context.Background(), opts, StepCompile, "[ERROR] boom\n", audit, &counter, 3, nil, "")

	if len(fixer.req.APISurface) != 0 {
		t.Error("no provider must mean no surface")
	}
	if audit.hasStep("evaluator.fix_api_surface_unavailable") {
		t.Error("an unconfigured provider is not a degradation and must not be audited as one")
	}
}

func TestRepoDeclaredTypeNames(t *testing.T) {
	got := repoDeclaredTypeNames(map[string]string{
		"src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTest.java": "",
		"src/main/java/org/springframework/samples/petclinic/owner/Owner.java":               "",
		"pom.xml": "",
	})
	for _, want := range []string{
		"org.springframework.samples.petclinic.PetClinicRuntimeHintsTest",
		"org.springframework.samples.petclinic.owner.Owner",
	} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("non-Java paths leaked in: %v", got)
	}
}
