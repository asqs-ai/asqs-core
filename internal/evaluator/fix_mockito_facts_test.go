package evaluator

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// The fixtures reproduce run api-12aa1935d113c9ea8b50a516fd275660's stuck failures verbatim in
// shape: when()/given() on receivers the test constructs with `new`, and strict-stub stubbings
// the tested code never consumes. Six fixer rounds saw these exceptions and repaired around them;
// the facts name the misuse deterministically.

const ownerTestsSrc = `package org.springframework.samples.petclinic.owner;

import static org.mockito.Mockito.when;

class OwnerTests {

	@Mock
	private Pet pet1;

	private Owner owner;

	@BeforeEach
	void setUp() {
		owner = new Owner();
	}

	@Test
	void addVisit_WhenPetExists_AddsVisitToPet() {
		Integer petId = 1;
		when(owner.getPet(petId)).thenReturn(pet1);
		owner.addVisit(petId, new Visit());
	}
}
`

func ownerTestsFiles() (map[string]string, []string) {
	rel := "src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java"
	return map[string]string{rel: ownerTestsSrc}, []string{rel}
}

func lineOf(t *testing.T, src, needle string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("needle %q not in source", needle)
	return 0
}

func TestTestFailureFacts_stubOnNonMock(t *testing.T) {
	files, arts := ownerTestsFiles()
	stubLine := lineOf(t, ownerTestsSrc, "when(owner.getPet(petId))")
	out := `[ERROR] addVisit_WhenPetExists_AddsVisitToPet  Time elapsed: 0.01 s  <<< ERROR!
org.mockito.exceptions.misusing.MissingMethodInvocationException:
when() requires an argument which has to be 'a method call on a mock'.
For example:
    when(mock.getArticles()).thenReturn(articles);

	at org.springframework.samples.petclinic.owner.OwnerTests.addVisit_WhenPetExists_AddsVisitToPet(OwnerTests.java:` + itoa(stubLine) + `)
`
	facts := testFailureFacts(context.Background(), StepTest, out, files, arts, nil)
	if len(facts) != 1 {
		t.Fatalf("facts = %v, want exactly one", facts)
	}
	for _, want := range []string{"`owner` is NOT a mock", "MissingMethodInvocationException", "OwnerTests.java"} {
		if !strings.Contains(facts[0], want) {
			t.Errorf("fact missing %q:\n%s", want, facts[0])
		}
	}
}

// WrongTypeOfReturnValue is the same defect wearing a misleading message (Mockito blames whichever
// mock invocation was recorded last); the fact must name the real receiver anyway.
func TestTestFailureFacts_wrongTypeOfReturnValueIsStubOnNonMock(t *testing.T) {
	files, arts := ownerTestsFiles()
	stubLine := lineOf(t, ownerTestsSrc, "when(owner.getPet(petId))")
	out := `org.mockito.exceptions.misusing.WrongTypeOfReturnValue:
UnmodifiableSet cannot be returned by findPetTypes()
findPetTypes() should return List
	at org.springframework.samples.petclinic.owner.OwnerTests.addVisit_WhenPetExists_AddsVisitToPet(OwnerTests.java:` + itoa(stubLine) + `)
`
	facts := testFailureFacts(context.Background(), StepTest, out, files, arts, nil)
	if len(facts) != 1 || !strings.Contains(facts[0], "`owner` is NOT a mock") {
		t.Fatalf("facts = %v", facts)
	}
}

func TestTestFailureFacts_unnecessaryStubbingSites(t *testing.T) {
	files, arts := ownerTestsFiles()
	out := `org.mockito.exceptions.misusing.UnnecessaryStubbingException:
Unnecessary stubbings detected.
Clean & maintainable test code requires zero unnecessary code.
Following stubbings are unnecessary (click to navigate to relevant line of code):
  1. -> at org.springframework.samples.petclinic.owner.OwnerTests.getPetByName_WhenPetExists_ReturnsPet(OwnerTests.java:41)
  2. -> at org.springframework.samples.petclinic.owner.OwnerTests.getPetByName_WhenPetExistsIgnoreCase_ReturnsPet(OwnerTests.java:53)
Please remove unnecessary stubbings or use 'lenient' strictness.
`
	facts := testFailureFacts(context.Background(), StepTest, out, files, arts, nil)
	if len(facts) != 2 {
		t.Fatalf("facts = %v, want two (one per cited site)", facts)
	}
	if !strings.Contains(facts[0], "OwnerTests.java:41") || !strings.Contains(facts[1], "OwnerTests.java:53") {
		t.Errorf("sites lost: %v", facts)
	}
	for _, f := range facts {
		if !strings.Contains(f, "never used by the code under test") {
			t.Errorf("fact must explain the defect: %s", f)
		}
	}
}

// The proof bar is one-sided: a receiver with ANY mock/spy evidence stays silent, whatever the
// exception says — a false "X is not a mock" would be worse than the blind rounds this prevents.
func TestTestFailureFacts_silentWithoutProof(t *testing.T) {
	rel := "src/test/java/p/T.java"
	arts := []string{rel}
	base := `class T {
	void t() {
		when(x.foo()).thenReturn(1);
	}
}
`
	out := `org.mockito.exceptions.misusing.MissingMethodInvocationException:
when() requires an argument which has to be 'a method call on a mock'.
	at p.T.t(T.java:3)
`
	for _, tc := range []struct {
		name string
		decl string
	}{
		{"@Mock annotated", "@Mock\nprivate X x;\n"},
		{"@MockitoBean annotated", "@MockitoBean\nprivate X x;\n"},
		{"mock() assigned", "private X x = mock(X.class);\n"},
		{"spy() assigned", "private X x = Mockito.spy(new X());\n"},
		{"no new-assignment evidence at all", "private X x;\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{rel: "class T {\n" + tc.decl + strings.TrimPrefix(base, "class T {\n")}
			if facts := testFailureFacts(context.Background(), StepTest, out, files, arts, nil); len(facts) != 0 {
				t.Fatalf("must stay silent without proof, got %v", facts)
			}
		})
	}
}

func TestTestFailureFacts_scopeAndStepGates(t *testing.T) {
	files, arts := ownerTestsFiles()
	out := "org.mockito.exceptions.misusing.MissingMethodInvocationException:\n\tat org.x.OwnerTests.t(OwnerTests.java:22)\n"
	if facts := testFailureFacts(context.Background(), StepCompile, out, files, arts, nil); facts != nil {
		t.Errorf("compile step must be a no-op, got %v", facts)
	}
	if facts := testFailureFacts(context.Background(), StepTest, "ordinary assertion failure", files, arts, nil); facts != nil {
		t.Errorf("no Mockito exception, no facts: %v", facts)
	}
	// A frame into a non-artifact file proves nothing about generated tests.
	other := "org.mockito.exceptions.misusing.MissingMethodInvocationException:\n\tat org.x.HelperTests.t(HelperTests.java:22)\n"
	if facts := testFailureFacts(context.Background(), StepTest, other, files, arts, nil); facts != nil {
		t.Errorf("non-artifact frames must stay silent, got %v", facts)
	}
}

// Frames past the next failure block's marker belong to a different defect.
func TestTestFailureFacts_scanStopsAtNextBlock(t *testing.T) {
	files, arts := ownerTestsFiles()
	out := `org.mockito.exceptions.misusing.UnnecessaryStubbingException:
Following stubbings are unnecessary:
  1. -> at org.x.OwnerTests.a(OwnerTests.java:41)
[ERROR] other  <<< ERROR!
org.mockito.exceptions.misusing.MissingMethodInvocationException:
	at org.x.OwnerTests.b(OwnerTests.java:22)
`
	facts := testFailureFacts(context.Background(), StepTest, out, files, arts, nil)
	for _, f := range facts {
		if strings.Contains(f, "OwnerTests.java:22") && strings.Contains(f, "never used") {
			t.Fatalf("frame from the NEXT block misattributed to unnecessary-stubbing: %v", facts)
		}
	}
}

func TestTestFailureFacts_auditsWhenStated(t *testing.T) {
	files, arts := ownerTestsFiles()
	stubLine := lineOf(t, ownerTestsSrc, "when(owner.getPet(petId))")
	out := "org.mockito.exceptions.misusing.MissingMethodInvocationException:\n\tat org.x.OwnerTests.t(OwnerTests.java:" + itoa(stubLine) + ")\n"
	audit := &recordingAuditor{}
	if facts := testFailureFacts(context.Background(), StepTest, out, files, arts, audit); len(facts) == 0 {
		t.Fatal("expected a fact")
	}
	if len(audit.payloads["evaluator.fix_test_failure_facts"]) != 1 {
		t.Fatalf("facts must be audited, steps: %v", audit.steps)
	}
}

func TestReceiverProvablyNotMock(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"new assignment", "owner = new Owner();", true},
		{"annotation binds to ITS field only", "@Mock\nprivate Pet pet1;\nprivate Owner owner;\nvoid s(){ owner = new Owner(); }", true},
		{"annotated receiver", "@Mock\nprivate Owner owner;", false},
		{"mock assigned", "owner = mock(Owner.class);", false},
		{"both new and mock evidence", "owner = new Owner();\nowner = spy(owner);", false},
		{"no evidence", "private Owner owner;", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := receiverProvablyNotMock(tc.src, "owner"); got != tc.want {
				t.Errorf("receiverProvablyNotMock = %v, want %v", got, tc.want)
			}
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
