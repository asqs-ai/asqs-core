package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// petControllerTests mirrors the shape that stalled run api-4f92fec6985aee5e4ce48de0041747d2:
// an outer test class with a @BeforeEach setup() and an existing @Nested class for the very
// method the next gap targets.
const petControllerTests = `package org.springframework.samples.petclinic.owner;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;

class PetControllerTests {

	@BeforeEach
	void setup() {
	}

	@Nested
	class ProcessUpdateFormHasErrors {

		@Test
		void rejectsBlankName() {
		}

	}

}
`

func writeExisting(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return full
}

const petControllerRel = "src/test/java/org/springframework/samples/petclinic/owner/PetControllerTests.java"

// javac rejected the merged file with "class ProcessUpdateFormHasErrors is already defined".
// dropDuplicateMembers could not see it: javaMethodDeclRE needs a parameter list and
// javaFieldDeclRE needs a `;`, so a nested TYPE matched neither.
func TestExtendMerge_dropsNestedTypeAlreadyDefined(t *testing.T) {
	dir := t.TempDir()
	full := writeExisting(t, dir, petControllerRel, petControllerTests)

	payload := `	@Nested
	class ProcessUpdateFormHasErrors {

		@Test
		void rejectsInvalidBirthDate() {
		}

	}

	@Nested
	class ProcessUpdateFormSuccess {

		@Test
		void redirectsOnSuccess() {
		}

	}
`
	n, _, _ := Write(dir, []Item{{
		Path: petControllerRel, Content: payload, ExtendExisting: true,
		SourceSymbolFile: "src/main/java/org/springframework/samples/petclinic/owner/PetController.java",
	}})
	if n != 1 {
		t.Fatalf("expected the merge to be written, wrote %d", n)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(string(got), "class ProcessUpdateFormHasErrors"); c != 1 {
		t.Errorf("ProcessUpdateFormHasErrors declared %d times, want 1:\n%s", c, got)
	}
	if !strings.Contains(string(got), "class ProcessUpdateFormSuccess") {
		t.Error("the non-colliding nested class must survive")
	}
	if strings.Contains(string(got), "rejectsInvalidBirthDate") {
		t.Error("the colliding type's body must be removed with it, not orphaned into the outer class")
	}
}

// A payload that repeats its own nested class is the same compile error from a different cause,
// and dropDuplicateMembers never compared the payload against itself.
func TestExtendMerge_dropsNestedTypeRepeatedWithinPayload(t *testing.T) {
	dir := t.TempDir()
	full := writeExisting(t, dir, petControllerRel, petControllerTests)

	payload := `	@Nested
	class WhenPetIsNew {

		@Test
		void first() {
		}

	}

	@Nested
	class WhenPetIsNew {

		@Test
		void second() {
		}

	}
`
	if n, _, _ := Write(dir, []Item{{
		Path: petControllerRel, Content: payload, ExtendExisting: true,
	}}); n != 1 {
		t.Fatal("expected the merge to be written")
	}
	got, _ := os.ReadFile(full)
	if c := strings.Count(string(got), "class WhenPetIsNew"); c != 1 {
		t.Errorf("WhenPetIsNew declared %d times, want 1:\n%s", c, got)
	}
	if !strings.Contains(string(got), "void first()") {
		t.Error("the FIRST copy is the one to keep")
	}
}

// The method sweep used to be flat over the whole file, so a payload nested class carrying its own
// setup() lost it to the outer class's @BeforeEach setup() — leaving a @Nested class whose fixture
// silently vanished.
func TestExtendMerge_keepsNestedScopedMethodSharingAnOuterName(t *testing.T) {
	dir := t.TempDir()
	full := writeExisting(t, dir, petControllerRel, petControllerTests)

	payload := `	@Nested
	class WhenOwnerMissing {

		@BeforeEach
		void setup() {
		}

		@Test
		void returnsNotFound() {
		}

	}
`
	if n, _, _ := Write(dir, []Item{{
		Path: petControllerRel, Content: payload, ExtendExisting: true,
	}}); n != 1 {
		t.Fatal("expected the merge to be written")
	}
	got := string(mustRead(t, full))
	if c := strings.Count(got, "void setup()"); c != 2 {
		t.Errorf("want the outer setup() and the nested one, found %d:\n%s", c, got)
	}
}

// A payload method at the OUTER level that duplicates an outer method must still be dropped —
// depth-scoping must not weaken the check it was already doing.
func TestExtendMerge_stillDropsOuterLevelDuplicateMethod(t *testing.T) {
	dir := t.TempDir()
	full := writeExisting(t, dir, petControllerRel, petControllerTests)

	payload := `	@BeforeEach
	void setup() {
	}

	@Test
	void somethingNew() {
	}
`
	if n, _, _ := Write(dir, []Item{{
		Path: petControllerRel, Content: payload, ExtendExisting: true,
	}}); n != 1 {
		t.Fatal("expected the merge to be written")
	}
	got := string(mustRead(t, full))
	if c := strings.Count(got, "void setup()"); c != 1 {
		t.Errorf("the duplicate outer setup() should have been dropped, found %d:\n%s", c, got)
	}
	if !strings.Contains(got, "void somethingNew()") {
		t.Error("the new test must survive")
	}
}

// When dedup removes everything, the old code wrote the file back unchanged and counted it as
// extended. That reports coverage the run did not gain.
func TestExtendMerge_skipsWhenPayloadIsEntirelyDuplicate(t *testing.T) {
	dir := t.TempDir()
	full := writeExisting(t, dir, petControllerRel, petControllerTests)
	before := mustRead(t, full)

	payload := `	@Nested
	class ProcessUpdateFormHasErrors {

		@Test
		void rejectsBlankName() {
		}

	}
`
	n, paths, skips := Write(dir, []Item{{
		Path: petControllerRel, Content: payload, ExtendExisting: true,
	}})
	if n != 0 || len(paths) != 0 {
		t.Errorf("an all-duplicate payload must not count as a write, got n=%d paths=%v", n, paths)
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "already-defined") {
		t.Errorf("the skip must be reported for the audit, got %v", skips)
	}
	if got := mustRead(t, full); string(got) != string(before) {
		t.Error("the file must be left untouched")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
