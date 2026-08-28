package evaluator

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
)

const vetsSource = `package org.springframework.samples.petclinic.vet;

import java.util.ArrayList;
import java.util.List;

public class Vets {

	private List<Vet> vets;

	public List<Vet> getVetList() {
		if (vets == null) {
			vets = new ArrayList<>();
		}
		return vets;
	}

}
`

const visitControllerTestsSource = `package org.springframework.samples.petclinic.owner;

class VisitControllerTests {
}
`

// The exact diagnostic shape of run api-7549a0ea57f8950449087ff85f1c4ce6: a method invented on a
// repo-owned dependency type (setVets on Vets) and a bare call with no static import in the test
// class itself (notNull).
const stalledRunErrorOutput = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java:[101,17] cannot find symbol
  symbol:   method notNull(org.springframework.samples.petclinic.owner.Visit)
  location: class org.springframework.samples.petclinic.owner.VisitControllerTests
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/vet/VetsTests.java:[34,21] cannot find symbol
  symbol:   method setVets(java.util.List<org.springframework.samples.petclinic.vet.Vet>)
  location: variable vets of type org.springframework.samples.petclinic.vet.Vets
`

type stubSurfaceProvider struct {
	surfaces []apisurface.TypeSurface
	targets  []apisurface.Target
}

func (s *stubSurfaceProvider) Lookup(_ context.Context, _ string, targets []apisurface.Target) ([]apisurface.TypeSurface, error) {
	s.targets = append(s.targets, targets...)
	return s.surfaces, nil
}

func factsFixtureFiles() map[string]string {
	return map[string]string{
		"src/main/java/org/springframework/samples/petclinic/vet/Vets.java":                   vetsSource,
		"src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java": visitControllerTestsSource,
	}
}

func TestMissingMemberFacts_ownedTypeAndTestClass(t *testing.T) {
	opts := DefaultEvalOptions(t.TempDir(), "java")
	opts.APISurfaceProvider = &stubSurfaceProvider{surfaces: []apisurface.TypeSurface{{
		FQCN:           "org.springframework.util.Assert",
		AllMemberNames: []string{"notNull", "isTrue"},
	}}}
	audit := &recordingAuditor{}
	facts := missingMemberFacts(context.Background(), opts, stalledRunErrorOutput, factsFixtureFiles(),
		[]string{"src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java"}, audit)
	if len(facts) != 2 {
		t.Fatalf("want 2 facts, got %d: %v", len(facts), facts)
	}
	joined := strings.Join(facts, "\n")
	// Owned dependency type: negative fact + real alternatives from source.
	if !strings.Contains(joined, `has NO method "setVets"`) {
		t.Errorf("missing the setVets negative fact:\n%s", joined)
	}
	if !strings.Contains(joined, "getVetList") {
		t.Errorf("alternatives should list getVetList from Vets.java:\n%s", joined)
	}
	// Test-class bare call: negative fact + classpath-verified static import + JUnit fallback.
	if !strings.Contains(joined, `No method "notNull" is defined in VisitControllerTests`) {
		t.Errorf("missing the notNull test-class fact:\n%s", joined)
	}
	if !strings.Contains(joined, "import static org.springframework.util.Assert.notNull;") {
		t.Errorf("expected the classpath-verified static import suggestion:\n%s", joined)
	}
	if !strings.Contains(joined, "org.junit.jupiter.api.Assertions") {
		t.Errorf("expected the JUnit rewrite fallback:\n%s", joined)
	}
	if len(audit.payloads["evaluator.fix_missing_member_facts"]) != 1 {
		t.Error("expected one fix_missing_member_facts audit event")
	}
}

func TestMissingMemberFacts_thirdPartyTypesAreNotClaimed(t *testing.T) {
	// A miss on a NON-owned type must produce no fact: the classpath API surface owns that case,
	// and a source-less negative claim here could be wrong.
	errOut := `cannot find symbol
  symbol:   method hasStatusCode(int)
  location: variable response of type com.microsoft.playwright.APIResponse
`
	opts := DefaultEvalOptions(t.TempDir(), "java")
	facts := missingMemberFacts(context.Background(), opts, errOut, factsFixtureFiles(), nil, nil)
	if len(facts) != 0 {
		t.Fatalf("third-party miss must yield no facts, got %v", facts)
	}
}

func TestMissingMemberFacts_nonJavaIsNil(t *testing.T) {
	opts := DefaultEvalOptions(t.TempDir(), "typescript")
	if facts := missingMemberFacts(context.Background(), opts, stalledRunErrorOutput, factsFixtureFiles(), nil, nil); facts != nil {
		t.Fatalf("non-java must be nil, got %v", facts)
	}
}

func TestMissingMemberFacts_noProviderStillStatesNegativeFact(t *testing.T) {
	opts := DefaultEvalOptions(t.TempDir(), "java")
	opts.APISurfaceProvider = nil
	facts := missingMemberFacts(context.Background(), opts, stalledRunErrorOutput, factsFixtureFiles(),
		[]string{"src/test/java/org/springframework/samples/petclinic/owner/VisitControllerTests.java"}, nil)
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, `No method "notNull"`) {
		t.Errorf("negative fact must not depend on the provider:\n%s", joined)
	}
	if strings.Contains(joined, "import static") {
		t.Errorf("no provider => no import suggestions (a guessed import is worse than none):\n%s", joined)
	}
}

// The getPet stall of run api-7b38aac91623c962b588a0e0a9fbb2f6: an AMBIGUOUS reference produced
// the fact "Owner has NO method getPet — do not call it again" on all seven stuck rounds, because
// the merged apisurface targets carry ambiguity shapes too and the generator claimed non-existence
// for all of them. The method exists twice; the only correct repair keeps calling it.
const ambiguousGetPetErrorOutput = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java:[93,34] reference to getPet is ambiguous
  both method getPet(java.lang.String) in org.springframework.samples.petclinic.owner.Owner and method getPet(java.lang.Integer) in org.springframework.samples.petclinic.owner.Owner match
`

const ownerSource = `package org.springframework.samples.petclinic.owner;

public class Owner {

	public Pet getPet(String name) {
		return null;
	}

	public Pet getPet(Integer id) {
		return null;
	}

}
`

func TestMissingMemberFacts_ambiguityIsNotAMissingMethod(t *testing.T) {
	opts := DefaultEvalOptions(t.TempDir(), "java")
	files := map[string]string{
		"src/main/java/org/springframework/samples/petclinic/owner/Owner.java":      ownerSource,
		"src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java": "package org.springframework.samples.petclinic.owner;\nclass OwnerTests {}\n",
	}
	audit := &recordingAuditor{}
	facts := missingMemberFacts(context.Background(), opts, ambiguousGetPetErrorOutput, files,
		[]string{"src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java"}, audit)
	joined := strings.Join(facts, "\n")
	if strings.Contains(joined, "has NO method") || strings.Contains(joined, "No method") {
		t.Fatalf("an ambiguous reference must never yield a non-existence claim — the method exists twice:\n%s", joined)
	}
	if len(facts) != 1 {
		t.Fatalf("want exactly one TRUE ambiguity fact, got %d: %v", len(facts), facts)
	}
	for _, want := range []string{
		"AMBIGUOUS, not missing",
		"getPet(java.lang.String)",
		"getPet(java.lang.Integer)",
		"cast",
		"Do not remove the call",
	} {
		if !strings.Contains(facts[0], want) {
			t.Errorf("ambiguity fact missing %q:\n%s", want, facts[0])
		}
	}
	events := audit.payloads["evaluator.fix_missing_member_facts"]
	if len(events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(events))
	}
	if fs, _ := events[0]["facts"].([]string); len(fs) != 1 || !strings.Contains(fs[0], "(ambiguous-overload)") {
		t.Errorf("audit entry should mark the ambiguity provenance; got %v", fs)
	}
}

// A not-applicable overload ("method a.b.C.m(T) is not applicable") also proves the method EXISTS;
// it must not become a non-existence claim, and there is no deterministic remedy to state, so the
// block stays silent.
func TestMissingMemberFacts_notApplicableYieldsNoFalseClaim(t *testing.T) {
	errOut := `cannot find symbol? no — overload mismatch:
no suitable method found for getVetList(int)
    method org.springframework.samples.petclinic.vet.Vets.getVetList() is not applicable
`
	opts := DefaultEvalOptions(t.TempDir(), "java")
	facts := missingMemberFacts(context.Background(), opts, errOut, factsFixtureFiles(), nil, nil)
	if len(facts) != 0 {
		t.Fatalf("not-applicable overloads must yield no facts, got %v", facts)
	}
}

func TestJavaDeclaredMethodNames(t *testing.T) {
	got := javaDeclaredMethodNames(vetsSource)
	if len(got) != 1 || got[0] != "getVetList" {
		t.Fatalf("want [getVetList], got %v", got)
	}
	// Signature-sliced form (bodies elided) must still match, and constructors must not.
	sliced := "public class Vets {\n\tpublic Vets() { /* body elided for fixer context */ }\n\tpublic List<Vet> getVetList() { /* body elided for fixer context */ }\n}\n"
	got = javaDeclaredMethodNames(sliced)
	if len(got) != 1 || got[0] != "getVetList" {
		t.Fatalf("sliced form: want [getVetList], got %v", got)
	}
}
