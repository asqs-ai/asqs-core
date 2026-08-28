package evaluator

import (
	"strings"
	"testing"
)

// The defect that motivated the edit contract: `Set<Visit>` where the SUT returns
// `Collection<Visit>`, unchanged at PetTests.java:32 for seven rounds of whole-file regeneration.
const petTestsBody = `package p;

class PetTests {

	@Test
	void addVisit_shouldAddVisitToPetVisits() {
		// Arrange
		Set<Visit> initialVisits = pet.getVisits();
		notNull(initialVisits);
	}

	@Test
	void other() {
		Collection<Visit> visits = pet.getVisits();
	}
}
`

func TestApplyFixEdits_appliesTargetedChange(t *testing.T) {
	edits := []FixEdit{{
		Find:    "Set<Visit> initialVisits = pet.getVisits();",
		Replace: "Collection<Visit> initialVisits = pet.getVisits();",
	}}
	got, outcomes := ApplyFixEdits(petTestsBody, edits)
	applied, refused := DescribeEditOutcomes(outcomes)
	if applied != 1 || len(refused) != 0 {
		t.Fatalf("applied=%d refused=%v", applied, refused)
	}
	if !strings.Contains(got, "Collection<Visit> initialVisits") {
		t.Errorf("edit did not land:\n%s", got)
	}
	// The correct line eight lines down must be untouched.
	if strings.Count(got, "Collection<Visit> visits = pet.getVisits();") != 1 {
		t.Errorf("unrelated line disturbed:\n%s", got)
	}
}

// The property whole-file regeneration cannot provide: an edit that does not match is REPORTED,
// not silently absorbed into a file that looks like it was repaired.
func TestApplyFixEdits_unmatchedAnchorIsReported(t *testing.T) {
	_, outcomes := ApplyFixEdits(petTestsBody, []FixEdit{{
		Find:    "Set<Visit> initialVisits = pet.getAllVisits();", // does not exist
		Replace: "Collection<Visit> initialVisits = pet.getVisits();",
	}})
	applied, refused := DescribeEditOutcomes(outcomes)
	if applied != 0 {
		t.Fatal("a non-matching anchor must not count as applied")
	}
	if len(refused) != 1 || !strings.Contains(refused[0], "not found") {
		t.Errorf("refusal not explained: %v", refused)
	}
}

// An anchor that matches twice would edit an arbitrary occurrence. Refuse rather than guess.
func TestApplyFixEdits_ambiguousAnchorRefused(t *testing.T) {
	src := "a();\nb();\na();\n"
	got, outcomes := ApplyFixEdits(src, []FixEdit{{Find: "a();", Replace: "c();"}})
	applied, refused := DescribeEditOutcomes(outcomes)
	if applied != 0 || got != src {
		t.Fatalf("ambiguous anchor was applied: %q", got)
	}
	if len(refused) != 1 || !strings.Contains(refused[0], "appears 2 times") {
		t.Errorf("ambiguity not explained: %v", refused)
	}
}

// One bad anchor must not discard a round's other correct repairs.
func TestApplyFixEdits_partialSuccess(t *testing.T) {
	got, outcomes := ApplyFixEdits(petTestsBody, []FixEdit{
		{Find: "notMatchingAnything();", Replace: "x();"},
		{Find: "Set<Visit> initialVisits", Replace: "Collection<Visit> initialVisits"},
	})
	applied, refused := DescribeEditOutcomes(outcomes)
	if applied != 1 || len(refused) != 1 {
		t.Fatalf("applied=%d refused=%v", applied, refused)
	}
	if !strings.Contains(got, "Collection<Visit> initialVisits") {
		t.Errorf("the good edit was lost:\n%s", got)
	}
}

// Models reflow indentation when quoting an anchor; that alone must not lose a correct repair.
func TestApplyFixEdits_whitespaceTolerant(t *testing.T) {
	got, outcomes := ApplyFixEdits(petTestsBody, []FixEdit{{
		Find:    "Set<Visit> initialVisits = pet.getVisits();\nnotNull(initialVisits);",
		Replace: "Collection<Visit> initialVisits = pet.getVisits();\nnotNull(initialVisits);",
	}})
	applied, _ := DescribeEditOutcomes(outcomes)
	if applied != 1 {
		t.Fatalf("reflowed anchor was not matched; applied=%d", applied)
	}
	if !strings.Contains(got, "Collection<Visit> initialVisits") {
		t.Errorf("edit did not land:\n%s", got)
	}
	if !strings.Contains(got, "\t\tCollection<Visit>") {
		t.Errorf("indentation was not preserved:\n%s", got)
	}
}

func TestApplyFixEdits_emptyAnchor(t *testing.T) {
	src := "x();\n"
	got, outcomes := ApplyFixEdits(src, []FixEdit{{Find: "   ", Replace: "y();"}})
	applied, refused := DescribeEditOutcomes(outcomes)
	if applied != 0 || got != src || len(refused) != 1 {
		t.Errorf("empty anchor mishandled: applied=%d got=%q refused=%v", applied, got, refused)
	}
}
