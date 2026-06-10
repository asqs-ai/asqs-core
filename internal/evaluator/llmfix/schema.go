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
		Type:                 openaijson.Object,
		Description:          `Mapping from repo-relative paths of modified test artifact files to full file contents. Keys must match artifact paths from the prompt. String values use \n for newlines.`,
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
		props[k] = openaijson.Definition{
			Type:        openaijson.String,
			Description: "Full corrected file content (use \\n for newlines). Omit this key if you did not change this file.",
		}
	}
	d := &openaijson.Definition{
		Type:        openaijson.Object,
		Description: "Mapping of repo-relative artifact paths to full file content. Include only keys for files you actually changed; omit the rest.",
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
