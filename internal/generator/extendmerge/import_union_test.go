package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// petclinicVetTests mirrors the shape of the real file the run extended: a Java test class whose
// import block has no java.util.Set.
const petclinicVetTests = `package org.springframework.samples.petclinic.vet;

import org.junit.jupiter.api.Test;

import static org.assertj.core.api.Assertions.assertThat;

class VetTests {

	@Test
	void testSerialization() {
		assertThat(true).isTrue();
	}

}
`

func writeTemp(t *testing.T, rel, body string) (repo string) {
	t.Helper()
	repo = t.TempDir()
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func extendWith(t *testing.T, repo, rel, payload string) string {
	t.Helper()
	n, _, _ := Write(repo, []Item{{
		Path:             rel,
		Content:          payload,
		ExtendExisting:   true,
		SourceSymbolFile: "src/main/java/org/springframework/samples/petclinic/vet/Vet.java",
	}})
	if n != 1 {
		t.Fatalf("expected the extend write to land; wrote %d file(s)", n)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The exact failure from run api-d7e0cbece3e9260f73836f5d50d21c96:
// "cannot find symbol / symbol: class Set / location: class ...VetTests" at VetTests.java:64,
// because unwrapCompilationUnit discarded the payload's imports and nothing put them back.
func TestExtendExisting_unionsImportsFromMethodsOnlyPayload(t *testing.T) {
	rel := "src/test/java/org/springframework/samples/petclinic/vet/VetTests.java"
	repo := writeTemp(t, rel, petclinicVetTests)

	payload := `import java.util.HashSet;
import java.util.Set;

@Test
void getSpecialtiesInternal_returnsBackingSet() {
	Set<Specialty> s = new HashSet<>();
	assertThat(s).isEmpty();
}`

	got := extendWith(t, repo, rel, payload)

	for _, want := range []string{"import java.util.HashSet;", "import java.util.Set;"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged file is missing %q — the new method references a symbol it cannot resolve:\n%s", want, got)
		}
	}
	// The import must land in the import block, not inside the class body, where it is a syntax error.
	classAt := strings.Index(got, "class VetTests")
	for _, imp := range []string{"import java.util.Set;", "import java.util.HashSet;"} {
		if at := strings.Index(got, imp); at < 0 || at > classAt {
			t.Errorf("%q was spliced at/after the class declaration (idx %d vs class %d):\n%s", imp, at, classAt, got)
		}
	}
	if !strings.Contains(got, "getSpecialtiesInternal_returnsBackingSet") {
		t.Errorf("the new test method did not survive the merge:\n%s", got)
	}
	if strings.Count(got, "import org.junit.jupiter.api.Test;") != 1 {
		t.Errorf("existing import duplicated:\n%s", got)
	}
}

// A full compilation unit reaches the extend path whenever WriteCoordinator flips a second gap onto
// a path an earlier gap created. Its imports must survive the unwrap.
func TestExtendExisting_unionsImportsFromCompilationUnitPayload(t *testing.T) {
	rel := "src/test/java/org/springframework/samples/petclinic/vet/VetTests.java"
	repo := writeTemp(t, rel, petclinicVetTests)

	payload := `package org.springframework.samples.petclinic.vet;

import java.util.List;
import org.junit.jupiter.api.Test;

class VetTests {

	@Test
	void listsSpecialties() {
		List<String> names = List.of("radiology");
		assertThat(names).hasSize(1);
	}

}
`
	got := extendWith(t, repo, rel, payload)

	if !strings.Contains(got, "import java.util.List;") {
		t.Errorf("compilation-unit payload lost its imports on unwrap:\n%s", got)
	}
	if strings.Count(got, "package org.springframework.samples.petclinic.vet;") != 1 {
		t.Errorf("package line duplicated by the merge:\n%s", got)
	}
	if strings.Count(got, "class VetTests") != 1 {
		t.Errorf("class header spliced into the body:\n%s", got)
	}
	if !strings.Contains(got, "listsSpecialties") {
		t.Errorf("new method lost:\n%s", got)
	}
}

// javac reports `reference to assertThat is ambiguous / both method <T>assertThat(T) in
// Assertions and method <T>assertThat(T) in Assertions match` when a single static import and a
// matching on-demand static import are both present. The message names the same class on both
// sides and reads like a duplicate-jar problem — creating it while "fixing" imports would trade
// one failure for a much harder one.
func TestUnionImports_refusesOnDemandCollidingWithSingleImport(t *testing.T) {
	existing := parseImports("import static org.assertj.core.api.Assertions.assertThat;", ".java")
	incoming := parseImports("import static org.assertj.core.api.Assertions.*;", ".java")

	add, skipped := unionImports(existing, incoming, ".java")
	if len(add) != 0 {
		t.Errorf("added a colliding on-demand import: %v", add)
	}
	if len(skipped) != 1 {
		t.Errorf("collision not reported: %v", skipped)
	}
}

func TestUnionImports_dedupesAndRespectsOnDemandCoverage(t *testing.T) {
	existing := parseImports("import java.util.*;\nimport org.junit.jupiter.api.Test;", ".java")
	incoming := parseImports("import java.util.Set;\nimport org.junit.jupiter.api.Test;\nimport java.time.LocalDate;", ".java")

	add, skipped := unionImports(existing, incoming, ".java")
	if len(add) != 1 || add[0].path != "java.time.LocalDate" {
		t.Errorf("add = %v, want only java.time.LocalDate", add)
	}
	if len(skipped) != 2 {
		t.Errorf("expected java.util.Set (covered) and Test (duplicate) to be skipped; got %v", skipped)
	}
}

// Fail closed: a Java file with no package line and no imports gives no confident insertion point,
// and a merge known to be missing imports is worse than no merge.
func TestMergeImportsIntoFile_failsClosedWithoutAnchor(t *testing.T) {
	if _, ok := mergeImportsIntoFile("class T {}\n", parseImports("import java.util.Set;", ".java"), ".java"); ok {
		t.Error("expected fail-closed when no package line or import block exists")
	}
	src, ok := mergeImportsIntoFile("package p;\n\nclass T {}\n", parseImports("import java.util.Set;", ".java"), ".java")
	if !ok || !strings.Contains(src, "import java.util.Set;") {
		t.Errorf("package line should anchor the insertion; got ok=%v src=%q", ok, src)
	}
}

func TestHoistTopLevelImports(t *testing.T) {
	imports, rest := hoistTopLevelImports("A.java", "import java.util.Set;\n\n@Test\nvoid a() {}\n")
	if len(imports) != 1 || imports[0].path != "java.util.Set" {
		t.Fatalf("imports = %v", imports)
	}
	if strings.Contains(rest, "import ") {
		t.Errorf("import line left in the payload body: %q", rest)
	}
	if !strings.Contains(rest, "void a()") {
		t.Errorf("payload body lost: %q", rest)
	}
	// A members-only payload carrying imports must not be misread as a compilation unit, or the
	// unwrap finds no type body and the write is dropped entirely.
	if got := classifyExtendPayload("A.java", rest); got != payloadMembersOnly {
		t.Errorf("classify after hoist = %v, want members_only", got)
	}
}
