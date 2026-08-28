package apisurface

import (
	"os"
	"path/filepath"
	"testing"
)

func javaSourceRepo(t *testing.T, rels ...string) string {
	t.Helper()
	repo := t.TempDir()
	for _, rel := range rels {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package p;\nclass X {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestRepoDeclaredSimpleNames_collectsEverySourceRoot(t *testing.T) {
	repo := javaSourceRepo(t,
		"src/main/java/org/example/Vet.java",
		"src/test/java/org/example/VetTestFixtures.java",
		"src/it/java/org/example/VetIT.java",
		// Not source: a compiled artifact and a resource must not become type names.
		"target/classes/org/example/Vet.class",
		"src/main/resources/application.properties",
	)
	got, ok := RepoDeclaredSimpleNames(LangJava, repo)
	if !ok {
		t.Fatal("a Maven layout must be answerable")
	}
	for _, want := range []string{"Vet", "VetTestFixtures", "VetIT"} {
		if !got[want] {
			t.Errorf("%s is repo source but was not collected: %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("collected %v, want exactly the three .java files", got)
	}
}

// supported=false is the caller's instruction to make no absence claim at all. Getting this wrong in
// the permissive direction re-opens a false statement attached to a destructive directive.
func TestRepoDeclaredSimpleNames_refusesToTestifyWhenItCannot(t *testing.T) {
	full := javaSourceRepo(t, "src/main/java/org/example/Vet.java")

	if _, ok := RepoDeclaredSimpleNames(LangCSharp, full); ok {
		t.Error("C# sources live anywhere under the repo; the scan cannot answer for them")
	}
	if _, ok := RepoDeclaredSimpleNames(LangNode, full); ok {
		t.Error("TS/JS module names are not filenames; the scan cannot answer for them")
	}
	if _, ok := RepoDeclaredSimpleNames(LangJava, ""); ok {
		t.Error("no repo path, no testimony")
	}
	// A tree with no Maven/Gradle source root at all is a layout the scan does not understand.
	if _, ok := RepoDeclaredSimpleNames(LangJava, t.TempDir()); ok {
		t.Error("a repo with no java source root must not be reported as declaring nothing")
	}
}
