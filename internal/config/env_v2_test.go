package config

import (
	"reflect"
	"strings"
	"testing"
)

// Env names are derived, not tagged. These pin the derivation itself, because it is the contract
// every deployment's environment already depends on and a change here is invisible until a variable
// silently stops applying.
func TestEnvNameForPath(t *testing.T) {
	cases := map[string]string{
		// `general.` is stripped, which is what keeps the database and LLM variables identical to
		// their v1 spellings — the ones most likely to be in a running deployment already.
		"general.database.metadata_url": "ASQS_DATABASE_METADATA_URL",
		"general.llm.provider":          "ASQS_LLM_PROVIDER",
		"general.git.token":             "ASQS_GIT_TOKEN",
		// Anything outside `general` keeps its section.
		"generation.policy.tools.max_turns": "ASQS_GENERATION_POLICY_TOOLS_MAX_TURNS",
		"indexer.policy.max_gaps":           "ASQS_INDEXER_POLICY_MAX_GAPS",
		"fixer.iterations.max":              "ASQS_FIXER_ITERATIONS_MAX",
		"schema_version":                    "ASQS_SCHEMA_VERSION",
	}
	for path, want := range cases {
		if got := EnvNameForPath(path); got != want {
			t.Errorf("EnvNameForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// Client scoping is a multi-tenant deployment's whole mechanism for per-client overrides, and it is
// exactly the kind of layering that vanishes silently in a rewrite.
func TestEnvLookup_clientScopeBeatsGlobal(t *testing.T) {
	env := map[string]string{
		"ASQS_LLM_PROVIDER":      "openai",
		"ASQS_ACME_LLM_PROVIDER": "anthropic",
	}
	get := func(k string) string { return env[k] }

	if v, _ := envLookup(get, "ASQS_LLM_PROVIDER", "acme"); v != "anthropic" {
		t.Errorf("client-scoped lookup = %q, want the acme value", v)
	}
	if v, _ := envLookup(get, "ASQS_LLM_PROVIDER", ""); v != "openai" {
		t.Errorf("unscoped lookup = %q, want the global value", v)
	}
	// A client with no override of its own falls back to the global, rather than to nothing.
	if v, _ := envLookup(get, "ASQS_LLM_PROVIDER", "other"); v != "openai" {
		t.Errorf("unknown client = %q, want the global fallback", v)
	}
}

// v1's applyEnvStruct handled string/int/bool/*bool and nothing else, so float and slice variables
// were documented and inert. Every kind the schema actually uses must decode, and an unhandled kind
// must ERROR rather than be skipped — otherwise adding a field of a new type silently ships another
// inert variable.
func TestEnvV2_everyFieldTypeIsHandled(t *testing.T) {
	kinds := map[reflect.Kind]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			ft := rt.Field(i)
			if ft.PkgPath != "" || yamlFieldName(ft) == "" {
				continue
			}
			if ft.Type.Kind() == reflect.Struct {
				walk(ft.Type)
				continue
			}
			kinds[ft.Type.Kind()] = true
		}
	}
	walk(reflect.TypeOf(SchemaV2{}))

	for kind := range kinds {
		switch kind {
		case reflect.Map, reflect.Slice:
			continue // maps have no flat spelling; struct slices are skipped by the walker
		}
		probe := reflect.New(zeroOfKind(t, kind)).Elem()
		if err := setFieldFromEnv(probe, envSampleFor(kind)); err != nil {
			t.Errorf("kind %s is used by the schema but setFieldFromEnv rejects it: %v", kind, err)
		}
	}
}

func zeroOfKind(t *testing.T, k reflect.Kind) reflect.Type {
	t.Helper()
	switch k {
	case reflect.String:
		return reflect.TypeOf("")
	case reflect.Bool:
		return reflect.TypeOf(false)
	case reflect.Int:
		return reflect.TypeOf(0)
	case reflect.Int64:
		return reflect.TypeOf(int64(0))
	case reflect.Float64:
		return reflect.TypeOf(float64(0))
	case reflect.Ptr:
		return reflect.TypeOf((*bool)(nil))
	case reflect.Slice:
		return reflect.TypeOf([]string{})
	}
	t.Fatalf("the schema uses kind %s and this test does not know how to build one", k)
	return nil
}

func envSampleFor(k reflect.Kind) string {
	switch k {
	case reflect.Bool, reflect.Ptr:
		return "true"
	case reflect.Int, reflect.Int64:
		return "7"
	case reflect.Float64:
		return "0.5"
	}
	return "sample"
}

// The float and slice kinds specifically, because those are the two v1 documented and never applied.
func TestEnvV2_appliesFloatAndSlice(t *testing.T) {
	env := map[string]string{
		"ASQS_SANDBOX_RESOURCES_CPUS":                 "2.5",
		"ASQS_RETRIEVAL_POLICY_MIN_SIMILARITY_COSINE": "0.42",
		"ASQS_WEBSEARCH_ALLOWED_HOSTS":                "docs.example.com, *.rust-lang.org ,",
	}
	var s SchemaV2
	if err := ApplyV2Env(&s, func(k string) string { return env[k] }, ""); err != nil {
		t.Fatal(err)
	}
	if s.General.Sandbox.Resources.CPUs != 2.5 {
		t.Errorf("float env not applied: cpus = %v", s.General.Sandbox.Resources.CPUs)
	}
	if s.Retrieval.Policy.MinSimilarityCosine != 0.42 {
		t.Errorf("float env not applied: min_similarity_cosine = %v", s.Retrieval.Policy.MinSimilarityCosine)
	}
	want := []string{"docs.example.com", "*.rust-lang.org"}
	if !reflect.DeepEqual(s.General.WebSearch.AllowedHosts, want) {
		t.Errorf("slice env = %v, want %v (trailing comma and spaces dropped)", s.General.WebSearch.AllowedHosts, want)
	}
}

// A value the field cannot hold must fail loudly. Silently ignoring it is how a typo'd number
// becomes "the variable does nothing", which is the defect class this whole layer replaced.
func TestEnvV2_undecodableValueIsAnError(t *testing.T) {
	var s SchemaV2
	err := ApplyV2Env(&s, func(k string) string {
		if k == "ASQS_INDEXER_POLICY_MAX_GAPS" {
			return "twenty"
		}
		return ""
	}, "")
	if err == nil {
		t.Fatal("a non-numeric value for an int key was accepted silently")
	}
	if got := err.Error(); !strings.Contains(got, "ASQS_INDEXER_POLICY_MAX_GAPS") {
		t.Errorf("the error does not name the variable: %s", got)
	}
}

// THE ORDERING INVARIANT: YAML → env → defaults → translate.
//
// Applying defaults before the environment overlay would materialise a `true` for every default-true
// toggle, and the overlay would then have no way to tell "operator set false" from "unset variable".
// A default-true feature would become impossible to switch off from the environment — the single
// most consequential ordering bug this schema can have, and invisible in any test that only checks
// YAML.
func TestLoadOrder_envCanTurnOffADefaultTrueToggle(t *testing.T) {
	yamlDoc := []byte("general:\n  database:\n    metadata_url: postgres://x\n")
	env := map[string]string{"ASQS_RETRIEVAL_POLICY_ABSTENTION_ENABLED": "false"}

	c, s, err := LoadSchemaV2(yamlDoc, func(k string) string { return env[k] }, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Retrieval.Policy.Abstention.Enabled == nil || *s.Retrieval.Policy.Abstention.Enabled {
		t.Fatal("the environment could not turn off a default-true toggle; defaults ran before env")
	}
	if !c.Retrieval.AbstentionDisabled {
		t.Error("the runtime config did not receive the disabled state")
	}
}

// And the same toggle, untouched, must still arrive as its documented default.
func TestLoadOrder_absentToggleKeepsItsDefault(t *testing.T) {
	c, _, err := LoadSchemaV2([]byte("general:\n  database:\n    metadata_url: postgres://x\n"), func(string) string { return "" }, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Retrieval.AbstentionDisabled {
		t.Error("an unmentioned default-true toggle arrived disabled")
	}
}

// Env must override YAML, not the other way round: that layering is what lets a container inject a
// credential over a committed file.
func TestLoadOrder_envOverridesYAML(t *testing.T) {
	yamlDoc := []byte("general:\n  database:\n    metadata_url: postgres://from-yaml\n  llm:\n    provider: openai\n")
	env := map[string]string{"ASQS_LLM_PROVIDER": "anthropic"}
	c, _, err := LoadSchemaV2(yamlDoc, func(k string) string { return env[k] }, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Provider != "anthropic" {
		t.Errorf("provider = %q; the environment must win over YAML", c.LLM.Provider)
	}
	if c.Database.MetadataURL != "postgres://from-yaml" {
		t.Errorf("metadata_url = %q; an unset variable must not blank a YAML value", c.Database.MetadataURL)
	}
}
