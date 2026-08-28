package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real VisitControllerTests.java from run api-7971201149c4eba06d5d17f26c9d74c4, trimmed to the
// fields the merge went on to redeclare.
const visitControllerTests = `package org.springframework.samples.petclinic.owner;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.web.servlet.MockMvc;

class VisitControllerTests {

	private static final int TEST_OWNER_ID = 1;

	private static final int TEST_PET_ID = 1;

	@Autowired
	private MockMvc mockMvc;

	@MockitoBean
	private OwnerRepository owners;

	@Test
	void testInitNewVisitForm() throws Exception {
	}

}
`

// javac: "variable TEST_OWNER_ID is already defined in class VisitControllerTests" ×4, in three
// separate files in one run. dropDuplicateMembers suppressed duplicate METHODS only, so a payload
// that restated the class's fields spliced them straight in.
func TestExtendExisting_dropsDuplicateFields(t *testing.T) {
	rel := "src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java"
	repo := writeTemp(t, rel, visitControllerTests)

	// A full-compilation-unit payload, which is what WriteCoordinator hands over when a second gap
	// lands on a path an earlier gap created.
	payload := `package org.springframework.samples.petclinic.owner;

import org.junit.jupiter.api.Test;

class VisitControllerTests {

	private static final int TEST_OWNER_ID = 1;

	private static final int TEST_PET_ID = 1;

	@Autowired
	private MockMvc mockMvc;

	@MockitoBean
	private OwnerRepository owners;

	@Test
	void testLoadPetWithVisitWhenOwnerNotFound() throws Exception {
	}

}
`
	n, _, _ := Write(repo, []Item{{
		Path:             rel,
		Content:          payload,
		ExtendExisting:   true,
		SourceSymbolFile: "src/main/java/org/springframework/samples/petclinic/owner/VisitController.java",
	}})
	if n != 1 {
		t.Fatalf("expected the extend write to land; wrote %d", n)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, field := range []string{"TEST_OWNER_ID", "TEST_PET_ID", "mockMvc", "owners"} {
		if n := strings.Count(got, field+" ="); n > 1 {
			t.Errorf("field %s declared %d times — javac reports \"already defined\":\n%s", field, n, got)
		}
	}
	if c := strings.Count(got, "private MockMvc mockMvc;"); c != 1 {
		t.Errorf("mockMvc declared %d times:\n%s", c, got)
	}
	if c := strings.Count(got, "private OwnerRepository owners;"); c != 1 {
		t.Errorf("owners declared %d times:\n%s", c, got)
	}
	// The decorator above a dropped field must go with it, or an orphan @Autowired is left dangling.
	if c := strings.Count(got, "@Autowired"); c != 1 {
		t.Errorf("@Autowired appears %d times; the annotation should be cut with its field:\n%s", c, got)
	}
	// The new test must still arrive.
	if !strings.Contains(got, "testLoadPetWithVisitWhenOwnerNotFound") {
		t.Errorf("new test method lost:\n%s", got)
	}
	if !strings.Contains(got, "testInitNewVisitForm") {
		t.Errorf("existing test method lost:\n%s", got)
	}
}

func TestDropDuplicateFields_keepsGenuinelyNewFields(t *testing.T) {
	existing := "class T {\n\tprivate static final int A = 1;\n}\n"
	payload := "\tprivate static final int A = 1;\n\n\tprivate static final int B = 2;\n\n\t@Test\n\tvoid t() {\n\t}\n"

	got, dropped := dropDuplicateMembers("T.java", existing, payload)
	if len(dropped) != 1 || dropped[0] != "A" {
		t.Errorf("dropped = %v, want [A]", dropped)
	}
	if !strings.Contains(got, "int B = 2;") {
		t.Errorf("a genuinely new field was removed:\n%s", got)
	}
	if strings.Contains(got, "int A = 1;") {
		t.Errorf("duplicate field survived:\n%s", got)
	}
	if !strings.Contains(got, "void t()") {
		t.Errorf("method lost:\n%s", got)
	}
}

// The field regex must not swallow ordinary statements inside a method body.
func TestDropDuplicateFields_ignoresStatements(t *testing.T) {
	existing := "class T {\n\tprivate MockMvc mockMvc;\n}\n"
	payload := "\t@Test\n\tvoid t() {\n\t\tmockMvc.perform(get(\"/x\"));\n\t\tint owners = 3;\n\t}\n"

	got, dropped := dropDuplicateMembers("T.java", existing, payload)
	if len(dropped) != 0 {
		t.Errorf("dropped %v from inside a method body", dropped)
	}
	if !strings.Contains(got, "mockMvc.perform") || !strings.Contains(got, "int owners = 3;") {
		t.Errorf("statements were cut out of the method body:\n%s", got)
	}
}

func TestDropDuplicateFields_csharp(t *testing.T) {
	existing := "public class T\n{\n    private readonly Mock<IRepo> _repo;\n}\n"
	payload := "    private readonly Mock<IRepo> _repo;\n\n    [Fact]\n    public void A()\n    {\n    }\n"

	got, dropped := dropDuplicateMembers("T.cs", existing, payload)
	if len(dropped) != 1 || dropped[0] != "_repo" {
		t.Errorf("dropped = %v, want [_repo]", dropped)
	}
	if strings.Contains(got, "private readonly Mock<IRepo> _repo;") {
		t.Errorf("duplicate C# field survived:\n%s", got)
	}
	if !strings.Contains(got, "public void A()") {
		t.Errorf("method lost:\n%s", got)
	}
}
