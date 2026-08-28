package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersionCurrent is the only schema version this binary parses. Absent is accepted and means
// "current"; anything else is rejected. One line of cost, and any future evolution has an anchor to
// branch on instead of guessing from the document's shape.
const SchemaVersionCurrent = 2

// v1SectionNames are the top-level keys that existed only in the pre-v2 layout. A document carrying
// one is almost certainly a v1 config rather than a v2 config with a typo, and saying so is far more
// useful than the generic unknown-field error — which, on a v1 file, fires on whichever section
// happens to come first.
var v1SectionNames = map[string]string{
	"vcs":       "general.git",
	"runner":    "general.build / general.sandbox / generation.policy / fixer / bootstrap",
	"websearch": "general.websearch",
	"database":  "general.database",
	"llm":       "general.llm",
	"audit":     "general.audit",
	// `indexer`, `retrieval` and `generation` exist in BOTH layouts, so their mere presence proves
	// nothing. Each is recognised by a child that only v1 has.
	"indexer":    "indexer.policy / indexer.docker / generation.docs.overview",
	"retrieval":  "retrieval.policy / retrieval.context",
	"generation": "generation.policy.tools",
}

// v1OnlyChildren distinguishes a section name that exists in both layouts. The key is the section;
// the value is a child that ONLY the v2 shape has, so its absence marks the document as v1.
//
// Without this, a v2 config would be misreported as v1 the moment it declared `retrieval:` — which
// is worse than the generic error, because the message would confidently send an operator to
// rewrite a file that was already correct.
var v1OnlyChildren = map[string]string{
	"generation": "policy",
	"retrieval":  "policy",
	"indexer":    "policy",
}

// UnmarshalSchemaV2 strictly decodes YAML into the v2 schema.
//
// Strict is the point. v1 used yaml.Unmarshal with no KnownFields, so a typo'd or misplaced key was
// silently ignored — an operator could set `retrieval.max_similair_tests` and get no error, no
// warning, and no effect. That is the same failure shape as the inert keys this restructure removed,
// except self-inflicted and invisible. With KnownFields on, an unknown key fails the load and names
// its own path.
func UnmarshalSchemaV2(data []byte) (*SchemaV2, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("config: empty yaml")
	}
	var s SchemaV2
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, wrapSchemaDecodeError(trimmed, err)
	}
	if s.SchemaVersion != 0 && s.SchemaVersion != SchemaVersionCurrent {
		return nil, fmt.Errorf("config: schema_version %d is not supported (this build reads version %d; omit the key to mean current)",
			s.SchemaVersion, SchemaVersionCurrent)
	}
	return &s, nil
}

// wrapSchemaDecodeError turns a strict-decode failure into a message an operator can act on. When
// the document looks like a pre-v2 config it says so and points at the template, because the raw
// "field vcs not found in type config.SchemaV2" reads like a bug in ASQS rather than a stale file.
func wrapSchemaDecodeError(data []byte, err error) error {
	if legacy := detectV1Sections(data); len(legacy) > 0 {
		return fmt.Errorf("config: this file uses the pre-v2 layout (found top-level %s). "+
			"There is no automatic migration — start from config.example.yaml and move your values across. "+
			"Underlying parse error: %w", strings.Join(legacy, ", "), err)
	}
	return fmt.Errorf("config: parse yaml: %w", err)
}

// detectV1Sections reports which pre-v2 top-level sections the document declares, sorted by
// appearance so the message names the first thing an operator will look for.
func detectV1Sections(data []byte) []string {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	var found []string
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		newHome, isV1 := v1SectionNames[key]
		if !isV1 {
			continue
		}
		// Sections that exist in both layouts are only evidence of v1 when the v2-only child is
		// missing.
		if child, ambiguous := v1OnlyChildren[key]; ambiguous && mappingHasChild(doc.Content[i+1], child) {
			continue
		}
		found = append(found, fmt.Sprintf("%q (now %s)", key, newHome))
	}
	return found
}

func mappingHasChild(n *yaml.Node, key string) bool {
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

// LoadSchemaV2 is the full pipeline for one YAML document: strict decode, environment overlay,
// defaults, then translation to the runtime shape. Validation is the caller's step, because the
// required-field rules differ by command (see ValidateMode).
//
// The order matters and is the order v1 used: YAML is the base, env overrides it, defaults fill what
// neither supplied. Applying defaults before env would make a defaulted-true toggle impossible to
// turn off from the environment.
func LoadSchemaV2(data []byte, getenv func(string) string, clientID string) (*Config, *SchemaV2, error) {
	s, err := UnmarshalSchemaV2(data)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(clientID) == "" {
		clientID = s.ClientID
	}
	if getenv != nil {
		if err := ApplyV2Env(s, getenv, clientID); err != nil {
			return nil, nil, err
		}
	}
	ApplyV2Defaults(s)
	c := TranslateV2ToRuntime(s)
	if id := strings.TrimSpace(clientID); id != "" {
		c.ClientID = id
	}
	return c, s, nil
}

// LoadSchemaV2FromEnvOnly builds a config with no YAML file at all — every value from the
// environment. Containers that inject settings as variables use this path.
func LoadSchemaV2FromEnvOnly(getenv func(string) string, clientID string) (*Config, *SchemaV2, error) {
	var s SchemaV2
	if getenv != nil {
		if err := ApplyV2Env(&s, getenv, clientID); err != nil {
			return nil, nil, err
		}
	}
	ApplyV2Defaults(&s)
	c := TranslateV2ToRuntime(&s)
	if id := strings.TrimSpace(clientID); id != "" {
		c.ClientID = id
	}
	return c, &s, nil
}

// osGetenv is the production environment reader; tests inject a map instead.
func osGetenv(name string) string { return os.Getenv(name) }
