package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Environment overrides for schema v2.
//
// Names are DERIVED from the YAML path, not tagged: `ASQS_` + the dotted path upper-cased with `.`
// as `_`, and a leading `general.` stripped. So general.database.metadata_url is
// ASQS_DATABASE_METADATA_URL (unchanged from v1), generation.concurrency is
// ASQS_GENERATION_CONCURRENCY, and fixer.rerun.interval is ASQS_FIXER_RERUN_INTERVAL.
//
// One rule replaces v1's hand-maintained `env:` tags, and that is what fixes the class of bug the
// tags produced: v1's applyEnvStruct handled string/int/int64/bool/*bool and nothing else, so the
// documented float64 variables (ASQS_RUNNER_JOB_CPUS, ASQS_RETRIEVAL_MIN_SIMILARITY_COSINE) and
// every slice-tagged variable except two hard-coded special cases did nothing at all. A derived name that
// the walker cannot decode is impossible by construction here, and
// TestEnvV2_everyFieldTypeIsHandled asserts it.

const envPrefixV2 = "ASQS_"

// EnvNameForPath is the exported derivation, for the rare consumer that reads a schema variable
// outside the loader and must not hard-code a name that a schema move would silently orphan.
func EnvNameForPath(path string) string { return envNameForPath(path) }

// envNameForPath derives the environment variable name for a dotted v2 YAML path.
func envNameForPath(path string) string {
	p := strings.TrimPrefix(path, "general.")
	return envPrefixV2 + strings.ToUpper(strings.ReplaceAll(p, ".", "_"))
}

// envLookup reads one variable, honouring the two-level client scheme: ASQS_<ClientID>_<KEY> wins
// over ASQS_<KEY>. Multi-tenant deployments depend on that layering, and it is the kind of thing
// that vanishes silently in a rewrite, so it lives in one function with its own test.
func envLookup(getenv func(string) string, name, clientID string) (string, bool) {
	if id := strings.TrimSpace(clientID); id != "" {
		scoped := envPrefixV2 + strings.ToUpper(id) + "_" + strings.TrimPrefix(name, envPrefixV2)
		if v := getenv(scoped); v != "" {
			return v, true
		}
	}
	if v := getenv(name); v != "" {
		return v, true
	}
	return "", false
}

// ApplyV2Env overlays environment variables onto the parsed schema. Values already set from YAML
// are overwritten: env is the higher-precedence layer, as it was in v1.
func ApplyV2Env(s *SchemaV2, getenv func(string) string, clientID string) error {
	if s == nil {
		return nil
	}
	return walkV2Fields(reflect.ValueOf(s).Elem(), "", func(fv reflect.Value, path string) error {
		raw, ok := envLookup(getenv, envNameForPath(path), clientID)
		if !ok {
			return nil
		}
		if err := setFieldFromEnv(fv, raw); err != nil {
			return fmt.Errorf("config: %s: %w", envNameForPath(path), err)
		}
		return nil
	})
}

// walkV2Fields visits every settable LEAF field of the v2 tree, reporting its dotted YAML path.
// Structs are descended into; slices of structs are not, because a list of credentials has no
// sensible single-variable spelling.
func walkV2Fields(v reflect.Value, prefix string, visit func(reflect.Value, string) error) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		fv, ft := v.Field(i), t.Field(i)
		if ft.PkgPath != "" || !fv.CanSet() {
			continue
		}
		name := yamlFieldName(ft)
		if name == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		switch fv.Kind() {
		case reflect.Struct:
			if err := walkV2Fields(fv, path, visit); err != nil {
				return err
			}
			continue
		case reflect.Map:
			// profile_budgets is the only map, and a per-profile budget table has no flat env
			// spelling. YAML is its only home; the reflection test below knows that.
			continue
		case reflect.Slice:
			if fv.Type().Elem().Kind() == reflect.Struct {
				continue // e.g. registry credentials
			}
		}
		if err := visit(fv, path); err != nil {
			return err
		}
	}
	return nil
}

// setFieldFromEnv decodes one environment value into one field.
//
// Every kind the v2 schema uses is handled, including the two v1 silently skipped: float64, and
// []string as a comma-separated list. An unhandled kind returns an error rather than being ignored,
// so adding a field of a new type to the schema fails loudly instead of shipping an inert variable.
func setFieldFromEnv(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, ok := parseEnvBool(raw)
		if !ok {
			return fmt.Errorf("expected a boolean, got %q", raw)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", raw)
		}
		fv.SetInt(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", raw)
		}
		fv.SetFloat(f)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element type %s", fv.Type().Elem())
		}
		fv.Set(reflect.ValueOf(splitEnvList(raw)))
	case reflect.Ptr:
		if fv.Type().Elem().Kind() != reflect.Bool {
			return fmt.Errorf("unsupported pointer type %s", fv.Type())
		}
		b, ok := parseEnvBool(raw)
		if !ok {
			return fmt.Errorf("expected a boolean, got %q", raw)
		}
		p := reflect.New(fv.Type().Elem())
		p.Elem().SetBool(b)
		fv.Set(p)
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

// splitEnvList parses a comma-separated list, dropping empties so a trailing comma is harmless.
func splitEnvList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// yamlFieldName returns the yaml key for a struct field, or "" when the field is not YAML-facing.
// Shared by the env walker, the golden renderer and the frozen-key reflection guard, so that all
// three agree on what a field's path is.
func yamlFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "-" {
		return ""
	}
	if i := strings.Index(tag, ","); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	return tag
}
