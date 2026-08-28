package llmfix

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

func schemaJSON(t *testing.T, s any) map[string]any {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return m
}

// Fix 1. buildFixUserMessage tells the model targeted edits are PREFERRED; both schemas used to
// forbid the `edits` key. That was inert until the Ollama client began sending the schema as the
// native `format` field, at which point the grammar made whole-file rewrites the only expressible
// answer — and run api-4f92fec6985aee5e4ce48de0041747d2 spent five rounds reproducing a ~200-line
// file with the blamed line intact.
func TestFixSchema_permitsEditsShape(t *testing.T) {
	req := evaluator.FixRequest{
		ArtifactPaths: []string{"src/test/java/p/FooIT.java"},
		Files:         map[string]string{"src/test/java/p/FooIT.java": "class FooIT {}"},
	}

	for _, tc := range []struct {
		name   string
		schema any
	}{
		{"per-request", newFixFilesStructuredSchemaForRequest(req).Schema},
		{"fallback", newFixFilesStructuredSchema().Schema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := schemaJSON(t, tc.schema)
			props, _ := m["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("schema has no properties: %v", m)
			}
			edits, ok := props[editsPropertyName].(map[string]any)
			if !ok {
				t.Fatalf("schema forbids the PREFERRED edits shape; properties = %v", props)
			}
			if edits["type"] != "object" {
				t.Errorf("edits type = %v, want object", edits["type"])
			}
			ap, ok := edits["additionalProperties"].(map[string]any)
			if !ok || ap["type"] != "array" {
				t.Fatalf("edits values must be arrays of find/replace: %v", edits["additionalProperties"])
			}
			items, _ := ap["items"].(map[string]any)
			iprops, _ := items["properties"].(map[string]any)
			for _, k := range []string{"find", "replace"} {
				if _, ok := iprops[k]; !ok {
					t.Errorf("edit item missing %q: %v", k, iprops)
				}
			}
		})
	}
}

// The whole-file fallback must survive: an edit cannot always express the change.
func TestFixSchema_stillPermitsWholeFile(t *testing.T) {
	req := evaluator.FixRequest{
		ArtifactPaths: []string{"src/test/java/p/FooIT.java"},
		Files:         map[string]string{"src/test/java/p/FooIT.java": "class FooIT {}"},
	}
	m := schemaJSON(t, newFixFilesStructuredSchemaForRequest(req).Schema)
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["src/test/java/p/FooIT.java"]; !ok {
		t.Fatalf("artifact path property lost: %v", props)
	}

	// The fallback schema keeps additionalProperties:{type:string} so arbitrary paths stay legal.
	fb := schemaJSON(t, newFixFilesStructuredSchema().Schema)
	ap, _ := fb["additionalProperties"].(map[string]any)
	if ap == nil || ap["type"] != "string" {
		t.Errorf("fallback must still accept path -> whole-file string: %v", fb["additionalProperties"])
	}
}

// A response in the edits shape must round-trip through the parser the schema now permits.
func TestFixSchema_editsShapeParses(t *testing.T) {
	raw := `{"edits": {"src/test/java/p/FooIT.java": [{"find": "RuntimeHints.ResourcesRegistry", "replace": "ResourceHints"}]}}`
	edits := parseFixEdits(raw)
	if edits == nil {
		t.Fatal("parseFixEdits rejected the shape the schema now allows")
	}
	got := edits["src/test/java/p/FooIT.java"]
	if len(got) != 1 || !strings.Contains(got[0].Find, "ResourcesRegistry") {
		t.Fatalf("edits = %+v", got)
	}
}
