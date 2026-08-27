package llmfix

import (
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"

	openaijson "github.com/sashabaranov/go-openai/jsonschema"
)

// FixFilesStructuredSchemaName is the response_format.json_schema.name sent to OpenAI-compatible APIs.
const FixFilesStructuredSchemaName = "asqs_evaluator_fix_files"

// maxStrictFixArtifactKeys caps how many artifact paths get explicit JSON Schema properties.
// Beyond this, fall back to open additionalProperties (models may still return {}).
const maxStrictFixArtifactKeys = 12

func newFixFilesStructuredSchema() *model.StructuredJSONSchema {
	d := &openaijson.Definition{
		Type: openaijson.Object,
		Description: `Either {"edits": {path: [{find, replace}]}} (preferred) or a mapping from repo-relative paths of ` +
			`modified test artifact files to full file contents. String values use \n for newlines.`,
		Properties: map[string]openaijson.Definition{
			editsPropertyName: editsPropertyDefinition(),
		},
		// Any other key is a repo-relative path whose value is the whole file. Without this the
		// fallback shape would be lost; without Properties above, `edits` would be rejected as a
		// non-string value.
		AdditionalProperties: openaijson.Definition{Type: openaijson.String},
	}
	return &model.StructuredJSONSchema{
		Name:        FixFilesStructuredSchemaName,
		Description: "Evaluator LLM fix: path → full corrected test file content.",
		// Strict false: OpenAI structured mode requires additionalProperties:false on every object; a path→string
		// map uses additionalProperties:{type:string}, which is rejected with strict:true and can yield 400 / bad JSON errors.
		Strict: false,
		Schema: d,
	}
}

// strictFixArtifactKeys returns repo-relative paths (as in req.Files) that are both artifacts and present in Files.
func strictFixArtifactKeys(req evaluator.FixRequest) []string {
	if len(req.Files) == 0 || len(req.ArtifactPaths) == 0 {
		return nil
	}
	byNorm := make(map[string]string)
	for k := range req.Files {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		byNorm[filepath.ToSlash(k)] = k
	}
	var out []string
	seen := make(map[string]bool)
	for _, ap := range req.ArtifactPaths {
		ap = strings.TrimSpace(filepath.ToSlash(ap))
		if ap == "" || seen[ap] {
			continue
		}
		if canon, ok := byNorm[ap]; ok {
			seen[ap] = true
			out = append(out, canon)
		}
	}
	return out
}

// newFixFilesStructuredSchemaForRequest builds a schema that lists known artifact paths as optional properties
// (when the set is small). We do not set JSON Schema "required" on those keys: requiring every property forces
// the model to emit a string value per path on each turn, which rewrites nearly all generated tests even when
// only one file failed. Empty objects are handled by the fixer repair turn and prompt.
func newFixFilesStructuredSchemaForRequest(req evaluator.FixRequest) *model.StructuredJSONSchema {
	keys := strictFixArtifactKeys(req)
	if len(keys) == 0 || len(keys) > maxStrictFixArtifactKeys {
		return newFixFilesStructuredSchema()
	}
	props := make(map[string]openaijson.Definition)
	for _, k := range keys {
		// The preferred shape must be expressible. AdditionalProperties is false below, so without an
		// explicit property a grammar-enforcing provider rejects {"edits": …} outright.
		props[editsPropertyName] = editsPropertyDefinition()
		props[k] = openaijson.Definition{
			Type:        openaijson.String,
			Description: "Full corrected file content (use \\n for newlines). Omit this key if you did not change this file.",
		}
	}
	d := &openaijson.Definition{
		Type:        openaijson.Object,
		Description: "Either \"edits\" with targeted find/replace edits (preferred), or repo-relative artifact paths mapped to full file content. Include only files you actually changed.",
		Properties:  props,
		// Required intentionally empty; see comment on newFixFilesStructuredSchemaForRequest.
		AdditionalProperties: false,
	}
	return &model.StructuredJSONSchema{
		Name:        FixFilesStructuredSchemaName,
		Description: "Evaluator LLM fix: optional per-path file content (subset of artifacts).",
		Strict:      false,
		Schema:      d,
	}
}

// editsPropertyName is the key carrying the targeted-edit shape. It must match parseFixEdits and
// the "PREFERRED — targeted edits" instruction in buildFixUserMessage.
const editsPropertyName = "edits"

// editsPropertyDefinition describes {"edits": {"path": [{"find": …, "replace": …}]}}.
//
// This exists because the schema and the prompt used to contradict each other. buildFixUserMessage
// tells the model targeted edits are PREFERRED and to "edit the exact line the error names", while
// both schemas below forbade the `edits` key — the per-request one via AdditionalProperties:false,
// the fallback via AdditionalProperties:{type:string}, which rejects an object value.
//
// That contradiction was inert for as long as no provider enforced the schema. Once the Ollama
// client began sending it as the native `format` field, the grammar made whole-file rewrites the
// ONLY expressible answer. Run api-4f92fec6985aee5e4ce48de0041747d2 is the result: five rounds, the
// model reproducing a ~200-line file each time, and evaluator.fix_primary_site_untouched reporting
// on four of them that the blamed line came back unchanged. Reproducing a whole file faithfully is
// the dominant task; changing one line inside it is a detail a small model drops.
func editsPropertyDefinition() openaijson.Definition {
	return openaijson.Definition{
		Type:        openaijson.Object,
		Description: "Targeted edits per repo-relative artifact path. PREFERRED over whole-file content: edit the exact line the error names.",
		AdditionalProperties: openaijson.Definition{
			Type: openaijson.Array,
			Items: &openaijson.Definition{
				Type: openaijson.Object,
				Properties: map[string]openaijson.Definition{
					"find": {
						Type:        openaijson.String,
						Description: "Exact snippet copied from the file, appearing exactly once.",
					},
					"replace": {
						Type:        openaijson.String,
						Description: "Replacement snippet.",
					},
				},
				Required:             []string{"find", "replace"},
				AdditionalProperties: false,
			},
		},
	}
}
