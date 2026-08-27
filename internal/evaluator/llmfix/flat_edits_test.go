package llmfix

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// Run api-eb300211385b9616dc6cf81bd513369b died with skipped_reason=fixer_response_unusable on two
// consecutive rounds whose repair replies were {"edits": [{"find": …, "replace": …}]} — the
// contract's map shape with the path level dropped. Valid JSON, resolvable intent, classified
// not_json. These tests pin the recovery: the flat array parses, each edit resolves to the one
// artifact it can belong to, and anything genuinely ambiguous is refused rather than guessed.

func flatEditsReq() evaluator.FixRequest {
	return evaluator.FixRequest{
		ArtifactPaths: []string{
			"src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java",
			"src/test/java/org/springframework/samples/petclinic/owner/PetTests.java",
		},
		Files: map[string]string{
			"src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java": "class OwnerTests {\n\tvoid addVisit() {\n\t\towner.addVisit(2, visit);\n\t}\n}",
			"src/test/java/org/springframework/samples/petclinic/owner/PetTests.java":   "class PetTests {\n\tvoid isNew() {\n\t\tassertTrue(pet.isNew());\n\t}\n}",
		},
	}
}

func TestParseFixEditsAnyShape_mapShapeUnchanged(t *testing.T) {
	f := &Fixer{}
	raw := `{"edits": {"src/test/java/p/FooIT.java": [{"find": "a", "replace": "b"}]}}`
	got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), raw)
	if got == nil || len(got["src/test/java/p/FooIT.java"]) != 1 {
		t.Fatalf("map shape must keep working verbatim, got %+v", got)
	}
}

func TestParseFixEditsAnyShape_flatArrayResolvedByAnchor(t *testing.T) {
	f := &Fixer{}
	// Anchor text occurs only in OwnerTests.java.
	raw := `{"edits": [{"find": "owner.addVisit(2, visit);", "replace": "owner.addVisit(1, visit);"}]}`
	got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), raw)
	if got == nil {
		t.Fatal("flat edits array with a uniquely-resolvable anchor must parse")
	}
	edits, ok := got["src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java"]
	if !ok || len(edits) != 1 || edits[0].Replace != "owner.addVisit(1, visit);" {
		t.Fatalf("edit resolved to the wrong target: %+v", got)
	}
}

func TestParseFixEditsAnyShape_flatArrayWhitespaceTolerantResolution(t *testing.T) {
	f := &Fixer{}
	// The model reflowed the tabs to spaces; per-line-trimmed matching must still resolve it —
	// the same relaxation ApplyFixEdits itself retries with.
	raw := `{"edits": [{"find": "void addVisit() {\nowner.addVisit(2, visit);", "replace": "void addVisit() {\nowner.addVisit(1, visit);"}]}`
	got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), raw)
	if got == nil {
		t.Fatal("whitespace-reflowed anchor must still resolve to its only artifact")
	}
	if _, ok := got["src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java"]; !ok {
		t.Fatalf("resolved to wrong target: %+v", got)
	}
}

func TestParseFixEditsAnyShape_flatArrayPerItemPath(t *testing.T) {
	f := &Fixer{}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"path key", `{"edits": [{"path": "src/test/java/org/springframework/samples/petclinic/owner/PetTests.java", "find": "assertTrue(pet.isNew());", "replace": "assertFalse(pet.isNew());"}]}`},
		{"file key", `{"edits": [{"file": "src/test/java/org/springframework/samples/petclinic/owner/PetTests.java", "find": "assertTrue(pet.isNew());", "replace": "assertFalse(pet.isNew());"}]}`},
		{"basename resolves against artifact set", `{"edits": [{"path": "PetTests.java", "find": "assertTrue(pet.isNew());", "replace": "assertFalse(pet.isNew());"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), tc.raw)
			if got == nil {
				t.Fatal("flat edits with an explicit path must parse")
			}
			if _, ok := got["src/test/java/org/springframework/samples/petclinic/owner/PetTests.java"]; !ok {
				t.Fatalf("explicit path not honoured: %+v", got)
			}
		})
	}
}

func TestParseFixEditsAnyShape_singleArtifactTakesPathlessEdits(t *testing.T) {
	f := &Fixer{}
	req := evaluator.FixRequest{
		ArtifactPaths: []string{"src/test/java/p/OnlyIT.java"},
		Files:         map[string]string{"src/test/java/p/OnlyIT.java": "class OnlyIT {}"},
	}
	// Anchor does not even occur in the prompt copy; with one artifact in scope the target is
	// still unambiguous, and ApplyFixEdits re-judges the anchor against the disk bytes later.
	raw := `{"edits": [{"find": "int x = 1", "replace": "int x = 1;"}]}`
	got := f.parseFixEditsAnyShape(context.Background(), req, raw)
	if got == nil || len(got["src/test/java/p/OnlyIT.java"]) != 1 {
		t.Fatalf("single-artifact scope must adopt pathless edits, got %+v", got)
	}
}

func TestParseFixEditsAnyShape_ambiguousAnchorDropped(t *testing.T) {
	f := &Fixer{}
	req := flatEditsReq()
	// "class " occurs in both artifacts: resolution must refuse rather than pick one.
	raw := `{"edits": [{"find": "class ", "replace": "final class "}]}`
	if got := f.parseFixEditsAnyShape(context.Background(), req, raw); got != nil {
		t.Fatalf("ambiguous anchor must not be assigned, got %+v", got)
	}
}

func TestParseFixEditsAnyShape_controlCharsInsideStrings(t *testing.T) {
	f := &Fixer{}
	// Models quoting source write real newlines and tabs inside JSON strings; the flat shape gets
	// the same mechanical repair the map shape has.
	raw := "{\"edits\": [{\"find\": \"void addVisit() {\n\t\towner.addVisit(2, visit);\", \"replace\": \"void addVisit() {\n\t\towner.addVisit(1, visit);\"}]}"
	got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), raw)
	if got == nil {
		t.Fatal("raw control characters inside the flat shape must be repaired, not fatal")
	}
}

func TestParseFixEditsAnyShape_notEditsShapes(t *testing.T) {
	f := &Fixer{}
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"whole-file map", `{"src/test/java/p/FooIT.java": "class FooIT {}"}`},
		{"empty edits array", `{"edits": []}`},
		{"edits array with empty finds", `{"edits": [{"find": "  ", "replace": "x"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), tc.raw); got != nil {
				t.Fatalf("must fall through to the whole-file chain, got %+v", got)
			}
		})
	}
}

// The audit trail must say what actually happened: a recovered flat array is logged with the files
// it resolved to, and an unrecoverable one classifies as its own failure kind — the run that
// motivated this read "not_json" about a reply that was nothing but JSON.
func TestParseFixEditsAnyShape_auditsRecovery(t *testing.T) {
	aud := &captureAudit{}
	f := &Fixer{Audit: aud}
	raw := `{"edits": [{"find": "owner.addVisit(2, visit);", "replace": "owner.addVisit(1, visit);"}]}`
	if got := f.parseFixEditsAnyShape(context.Background(), flatEditsReq(), raw); got == nil {
		t.Fatal("expected recovery")
	}
	if !aud.has("llmfix.edits_array_recovered") {
		t.Fatalf("recovery must be audited, got steps %v", aud.steps())
	}
}

func TestClassifyFixParseFailure_editsArrayUnresolved(t *testing.T) {
	raw := `{"edits": [{"find": "void addVisit_WithInvalidPetId() {", "replace": "…"}]}`
	if got := classifyFixParseFailure(raw); got != "edits_array_unresolved" {
		t.Fatalf("classify = %q, want edits_array_unresolved", got)
	}
	// Regressions guard: the existing classes stay reachable.
	if got := classifyFixParseFailure(""); got != "empty_response" {
		t.Fatalf("classify(empty) = %q", got)
	}
	if got := classifyFixParseFailure(`{"a": "b"`); got != "truncated_json" {
		t.Fatalf("classify(truncated) = %q", got)
	}
}

func TestMatchArtifactPath(t *testing.T) {
	arts := []string{"src/test/a/FooTests.java", "src/test/b/BarTests.java"}
	for _, tc := range []struct{ claim, want string }{
		{"src/test/a/FooTests.java", "src/test/a/FooTests.java"},
		{"./src/test/a/FooTests.java", "src/test/a/FooTests.java"},
		{"FooTests.java", "src/test/a/FooTests.java"},
		{"NopeTests.java", ""},
	} {
		if got := matchArtifactPath(arts, tc.claim); got != tc.want {
			t.Errorf("matchArtifactPath(%q) = %q, want %q", tc.claim, got, tc.want)
		}
	}
	// Ambiguous basename refuses.
	if got := matchArtifactPath([]string{"a/T.java", "b/T.java"}, "T.java"); got != "" {
		t.Errorf("ambiguous basename must refuse, got %q", got)
	}
}

// captureAudit records steps for assertions.
type captureAudit struct{ logged []string }

func (c *captureAudit) Log(_ context.Context, step string, _ interface{}) {
	c.logged = append(c.logged, step)
}
func (c *captureAudit) has(step string) bool {
	for _, s := range c.logged {
		if s == step {
			return true
		}
	}
	return false
}
func (c *captureAudit) steps() string { return strings.Join(c.logged, ",") }
