package generator

import (
	"github.com/asqs/asqs-core/internal/intelligence/model"

	openaijson "github.com/sashabaranov/go-openai/jsonschema"
)

// GeneratedTestFilesStructuredSchemaName is the response_format.json_schema.name for first-pass test generation (distinct from the fixer schema).
const GeneratedTestFilesStructuredSchemaName = "asqs_generator_test_files"

func newGeneratedTestFilesStructuredSchema() *model.StructuredJSONSchema {
	d := &openaijson.Definition{
		Type:                 openaijson.Object,
		Description:          `Mapping from repo-relative test/spec file path(s) to full file contents. String values use \n for newlines.`,
		AdditionalProperties: openaijson.Definition{Type: openaijson.String},
	}
	return &model.StructuredJSONSchema{
		Name:        GeneratedTestFilesStructuredSchemaName,
		Description: "Generator: path → full generated test or spec file content.",
		Strict:      true,
		Schema:      d,
	}
}
