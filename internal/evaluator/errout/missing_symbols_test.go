package errout

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func petclinicRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	base := "src/main/java/org/springframework/samples/petclinic/owner/"
	writeFile(t, root, base+"PetType.java", "package org.springframework.samples.petclinic.owner;\npublic class PetType {}\n")
	writeFile(t, root, base+"Pet.java", "package org.springframework.samples.petclinic.owner;\npublic class Pet {}\n")
	writeFile(t, root, base+"PetValidator.java", "package org.springframework.samples.petclinic.owner;\npublic class PetValidator {}\n")
	// A test file with the same stem as a type must never be returned.
	writeFile(t, root, "src/test/java/org/springframework/samples/petclinic/owner/PetValidatorTest.java", "class PetValidatorTest {}\n")
	return root
}

func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// The PetClinic failure: PetType is imported from the wrong package (…model) so no FQCN resolves, yet
// the real class lives under …owner. The basename fallback must still find it.
func TestResolveMissingTypeFiles_bareNameWrongPackage(t *testing.T) {
	root := petclinicRepo(t)
	raw := `[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/PetValidatorTest.java:[13,51] cannot find symbol
  symbol:   class PetType
  location: package org.springframework.samples.petclinic.model`
	got := ResolveMissingTypeFiles(raw, root, "java", 8)
	want := "src/main/java/org/springframework/samples/petclinic/owner/PetType.java"
	if !contains(got, want) {
		t.Fatalf("ResolveMissingTypeFiles = %v; want it to contain %q", got, want)
	}
}

// `constructor Pet in class …owner.Pet cannot be applied` resolves by package (FQCN) and by stem.
func TestResolveMissingTypeFiles_constructorAndLocationClass(t *testing.T) {
	root := petclinicRepo(t)
	raw := `constructor Pet in class org.springframework.samples.petclinic.owner.Pet cannot be applied to given types;
  required: no arguments
  found:    long,java.lang.String
[ERROR] cannot find symbol
  symbol:   variable DOG
  location: class org.springframework.samples.petclinic.owner.PetType`
	got := ResolveMissingTypeFiles(raw, root, "java", 8)
	if !contains(got, "src/main/java/org/springframework/samples/petclinic/owner/Pet.java") {
		t.Errorf("missing Pet.java in %v", got)
	}
	if !contains(got, "src/main/java/org/springframework/samples/petclinic/owner/PetType.java") {
		t.Errorf("missing PetType.java in %v", got)
	}
}

// Test sources are never returned, JDK/built-in types never match (no repo file), and non-Java langs
// are a no-op (their compilers cite file paths directly).
func TestResolveMissingTypeFiles_excludesTestsBuiltinsAndOtherLangs(t *testing.T) {
	root := petclinicRepo(t)
	raw := `cannot find symbol
  symbol:   class PetValidatorTest
  symbol:   class Integer
  symbol:   class PetType`
	got := ResolveMissingTypeFiles(raw, root, "java", 8)
	for _, p := range got {
		if filepath.Base(p) == "PetValidatorTest.java" {
			t.Errorf("must not return test source: %v", got)
		}
		if filepath.Base(p) == "Integer.java" {
			t.Errorf("must not invent JDK type files: %v", got)
		}
	}
	if got := ResolveMissingTypeFiles(raw, root, "typescript", 8); got != nil {
		t.Errorf("non-Java lang should be a no-op; got %v", got)
	}
	if got := ResolveMissingTypeFiles(raw, root, "java", 0); got != nil {
		t.Errorf("limit<=0 should return nil; got %v", got)
	}
}

func TestResolveMissingTypeFiles_respectsLimit(t *testing.T) {
	root := petclinicRepo(t)
	raw := `symbol: class PetType
symbol: class Pet
symbol: class PetValidator`
	got := ResolveMissingTypeFiles(raw, root, "java", 1)
	if len(got) != 1 {
		t.Fatalf("limit=1 => %d paths (%v); want 1", len(got), got)
	}
}
