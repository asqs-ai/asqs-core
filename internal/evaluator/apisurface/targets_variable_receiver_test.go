package apisurface

import "testing"

// The primary diagnostic of run api-78ff1a4642435a35445718498d345f4b, verbatim. Before
// javacMethodOnVariable it yielded zero targets, so the fixer's API-surface block was built
// entirely from secondary errors while the blamed site contributed nothing.
const setNewDiagnostic = `[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java:[56,23] cannot find symbol
  symbol:   method setNew(boolean)
  location: variable newPet of type org.springframework.samples.petclinic.owner.Pet
`

func TestParseTargets_methodOnVariableReceiver(t *testing.T) {
	got := ParseTargets(setNewDiagnostic)
	if len(got) == 0 {
		t.Fatal("the blamed site produced no target at all; the fixer gets nothing about the receiver")
	}
	var found bool
	for _, tt := range got {
		if tt.Kind == KindType && tt.Name == "org.springframework.samples.petclinic.owner.Pet" {
			found = true
			if tt.Member != "setNew" {
				t.Errorf("member should be the rejected name so RankMembers can rank near-misses, got %q", tt.Member)
			}
		}
	}
	if !found {
		t.Errorf("receiver type not extracted, got %v", got)
	}
}

// A generic receiver resolves to the raw type, which is what javap accepts.
func TestParseTargets_methodOnGenericVariableReceiver(t *testing.T) {
	out := `  symbol:   method firstt()
  location: variable pets of type java.util.List<org.acme.Pet>
`
	got := ParseTargets(out)
	var found bool
	for _, tt := range got {
		if tt.Kind == KindType && tt.Name == "java.util.List" {
			found = true
		}
	}
	if !found {
		t.Errorf("want the raw receiver type java.util.List, got %v", got)
	}
}

// The class-receiver shape must keep working exactly as before.
func TestParseTargets_methodOnTypeStillWorks(t *testing.T) {
	out := `  symbol:   method hasURLContaining(java.lang.String)
  location: interface com.microsoft.playwright.assertions.PageAssertions
`
	got := ParseTargets(out)
	var found bool
	for _, tt := range got {
		if tt.Name == "com.microsoft.playwright.assertions.PageAssertions" && tt.Member == "hasURLContaining" {
			found = true
		}
	}
	if !found {
		t.Errorf("the pre-existing class-receiver shape regressed, got %v", got)
	}
}

// A repo-owned receiver is still dropped by ownership filtering — retrieval ships its source, so a
// javap dump would be duplicated budget. Extraction and filtering are separate jobs.
func TestParseTargets_variableReceiverStillSubjectToOwnershipFilter(t *testing.T) {
	got := ParseTargets(setNewDiagnostic)
	owned := map[string]bool{"org.springframework.samples.petclinic.owner.Pet": true}
	for _, tt := range FilterOwnedTypes(got, owned) {
		if tt.Kind == KindType && tt.Name == "org.springframework.samples.petclinic.owner.Pet" {
			t.Error("a repo-declared receiver must not survive ownership filtering")
		}
	}
}
