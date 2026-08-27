package config

import (
	"embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
)

// The generated configuration reference.
//
// Hand-maintaining a prose mirror of a 200-key struct is a losing game: upstream's exhaustive
// template had drifted about twenty fields away from the struct it claimed to describe while still
// being what operators copied from. The mirror is generated here instead, and a drift test fails the
// build when the checked-in copy goes stale.
//
// Doc comments are the reason this is not pure reflection: reflect cannot see them, and a reference
// without the explanation is just the struct again. The schema source is embedded and parsed for
// them, which keeps `qualitybot config reference` working from a shipped binary with no source tree.

// The schema's own source, plus the runtime file holding the leaf types v2 reuses verbatim —
// ShipConfig, PrivateRegistryCredential, RetrievalProfileBudget. Without it every reused key would
// render with an empty description.
//
//go:embed schema_v2.go config.go
var schemaSources embed.FS

// ReferenceEntry is one leaf key in the reference.
type ReferenceEntry struct {
	// Path is the dotted YAML path, e.g. "general.llm.embeddings.model".
	Path string
	// Section is the top-level block the key belongs to, for grouping.
	Section string
	// Type is the YAML-facing type as an operator would write it.
	Type string
	// Default is the effective value when the key is omitted, rendered as YAML would show it.
	Default string
	// Env is the derived environment variable name.
	Env string
	// Doc is the field's Go doc comment, flowed to one paragraph.
	Doc string
}

// BuildConfigReference walks the v2 schema and returns every leaf key with its documentation,
// type, effective default and derived environment variable, ordered by path within each section.
//
// Defaults come from an actual defaults pass over a zero schema rather than from a hand-written
// table, so a default that changes in code changes here too — which is the property that makes this
// document trustworthy in a way config-filled.example.yaml never was.
func BuildConfigReference() ([]ReferenceEntry, error) {
	docs, err := schemaFieldDocs()
	if err != nil {
		return nil, err
	}
	var withDefaults SchemaV2
	ApplyV2Defaults(&withDefaults)

	var out []ReferenceEntry
	v := reflect.ValueOf(&withDefaults).Elem()
	if err := walkV2FieldsAll(v, "", func(fv reflect.Value, path string) error {
		section := topLevelSection
		if i := strings.Index(path, "."); i > 0 {
			section = path[:i]
		}
		out = append(out, ReferenceEntry{
			Path:    path,
			Section: section,
			Type:    yamlTypeName(fv.Type()),
			Default: renderDefault(fv),
			Env:     envNameFor(fv, path),
			Doc:     docs[path],
		})
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// yamlTypeName renders a Go type the way an operator writing YAML would think of it.
func yamlTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		// A pointer bool exists only to distinguish "absent" from "false"; that is a schema
		// implementation detail, not something to explain in a reference.
		return yamlTypeName(t.Elem())
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Struct {
			// A list of blocks. Naming the Go type would send a reader looking for a struct they
			// cannot write; what they write is a YAML list of mappings.
			return "list of blocks"
		}
		return "list of " + yamlTypeName(t.Elem())
	case reflect.Map:
		return "mapping of " + yamlTypeName(t.Key()) + " to blocks"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.String:
		return "string"
	}
	return t.String()
}

// renderDefault shows the effective value for an omitted key. Empty strings and zero numbers are
// spelled out rather than left blank, because a blank cell reads as "unknown" rather than "empty".
func renderDefault(fv reflect.Value) string {
	switch fv.Kind() {
	case reflect.Ptr:
		if fv.IsNil() {
			return "unset"
		}
		return renderDefault(fv.Elem())
	case reflect.Slice, reflect.Map:
		if fv.Len() == 0 {
			return "empty"
		}
		return fmt.Sprintf("`%v`", fv.Interface())
	case reflect.String:
		if fv.String() == "" {
			return `` + "`\"\"`"
		}
		return fmt.Sprintf("`%q`", fv.String())
	default:
		return fmt.Sprintf("`%v`", fv.Interface())
	}
}

// schemaFieldDocs parses the embedded schema source and maps each dotted YAML path to its field's
// doc comment.
//
// It walks the type graph the same way the reflection walker does, but over the AST, so the two
// agree on what a path is. A field with no comment simply has no entry — the reference shows the
// gap rather than inventing prose for it.
func schemaFieldDocs() (map[string]string, error) {
	names, err := schemaSources.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("config: read embedded schema sources: %w", err)
	}
	structs := map[string]*ast.StructType{}
	fset := token.NewFileSet()
	for _, entry := range names {
		body, err := schemaSources.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", entry.Name(), err)
		}
		file, err := parser.ParseFile(fset, entry.Name(), body, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("config: parse embedded %s: %w", entry.Name(), err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				structs[ts.Name.Name] = st
			}
			return true
		})
	}

	docs := map[string]string{}
	// inherited carries the nearest ancestor field's doc. A leaf inside a small wrapper type —
	// EnabledV2's single `enabled`, StepLLMV2's four model fields — has nothing useful to say on its
	// own; what explains it is the field that introduced the wrapper. Without this,
	// `fixer.policy.multi_turn.enabled` would document itself as nothing, while the sentence that
	// actually describes it sits one level up on MultiTurn.
	var walk func(typeName, prefix, inherited string, seen map[string]bool)
	walk = func(typeName, prefix, inherited string, seen map[string]bool) {
		st, ok := structs[typeName]
		if !ok || seen[typeName] {
			return
		}
		seen[typeName] = true
		defer delete(seen, typeName)
		for _, f := range st.Fields.List {
			name := yamlNameFromASTTag(f.Tag)
			if name == "" || len(f.Names) == 0 {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			doc := flowComment(f.Doc)
			effective := doc
			if effective == "" {
				effective = inherited
			}
			if effective != "" {
				docs[path] = effective
			}
			if child := astTypeName(f.Type); child != "" {
				// Only a field's OWN doc is inherited downward; passing an inherited string further
				// would smear one sentence across a whole subtree.
				walk(child, path, doc, seen)
			}
		}
	}
	walk("SchemaV2", "", "", map[string]bool{})
	return docs, nil
}

// yamlNameFromASTTag extracts the yaml key from a struct tag literal.
func yamlNameFromASTTag(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	raw := strings.Trim(tag.Value, "`")
	st := reflect.StructTag(raw)
	name := st.Get("yaml")
	if name == "-" {
		return ""
	}
	if i := strings.Index(name, ","); i >= 0 {
		name = name[:i]
	}
	return name
}

// astTypeName returns the named type a field refers to, unwrapping pointers, slices and maps so a
// nested struct is followed wherever it appears.
func astTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return astTypeName(t.X)
	case *ast.ArrayType:
		return astTypeName(t.Elt)
	case *ast.MapType:
		return astTypeName(t.Value)
	}
	return ""
}

// flowComment turns a Go doc comment into one paragraph.
func flowComment(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	var parts []string
	for _, c := range g.List {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), " "))
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " ")
}

// envNameFor returns the derived variable, or the YAML-only marker for the two shapes the
// environment layer cannot express: a per-profile budget map and a list of registry credentials.
// Printing a plausible-looking variable for those would be worse than printing none — an operator
// would set it and get nothing.
func envNameFor(fv reflect.Value, path string) string {
	switch fv.Kind() {
	case reflect.Map:
		return "— (YAML only)"
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.Struct {
			return "— (YAML only)"
		}
	}
	return envNameForPath(path)
}
