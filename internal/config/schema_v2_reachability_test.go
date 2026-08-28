package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Every v2 key must demonstrably change the runtime config.
//
// The method is mechanical rather than a hand-written list, because a hand-written list is exactly
// what drifts: set one field to a distinctive value, translate, and require the resulting Config to
// differ from the baseline. A key the translator forgot produces an identical Config and fails here.
//
// **Stated limitation, because a test that overclaims is worse than none.** This catches keys the
// TRANSLATOR drops. It does NOT catch a key that reaches Config and is then read by nobody — that is
// TestEveryConfigFieldIsRead's job, and it has its own documented blind spot. Together they cover
// "YAML → Config" and "Config → consumer"; neither covers "consumer actually acts on it".
func TestEveryV2KeyReachesTheRuntime(t *testing.T) {
	baseline := TranslateV2ToRuntime(&SchemaV2{})

	var unreachable []string
	forEachV2Leaf(t, func(path string, set func(*SchemaV2, bool)) {
		// An ENUM string field legitimately ignores a value outside its vocabulary, so the generic
		// probe cannot move it and would report a perfectly wired key as dropped. These paths get a
		// value the field actually accepts. Keep the list short: every entry is a field whose
		// reachability is asserted with a hand-chosen value rather than mechanically.
		if v, ok := enumProbeValues[path]; ok {
			var s SchemaV2
			set(&s, true)
			setStringAt(&s, path, v)
			if reflect.DeepEqual(TranslateV2ToRuntime(&s), baseline) {
				unreachable = append(unreachable, path)
			}
			return
		}
		// A *bool is probed BOTH ways and counts as reachable if EITHER value moves the runtime.
		// Probing one way only mislabels every toggle whose default equals the probe value: writing
		// `false` into a default-off toggle produces exactly the absent config, and the key looks
		// dropped when it is merely agreeing with its default.
		for _, probe := range []bool{true, false} {
			var s SchemaV2
			set(&s, probe)
			if !reflect.DeepEqual(TranslateV2ToRuntime(&s), baseline) {
				return
			}
		}
		unreachable = append(unreachable, path)
	})

	// schema_version and client_id are metadata about the document, not settings the runtime shape
	// carries; client_id is applied by the loader after translation.
	allowed := map[string]bool{"schema_version": true, "client_id": true}
	var real []string
	for _, p := range unreachable {
		if !allowed[p] {
			real = append(real, p)
		}
	}
	if len(real) > 0 {
		sort.Strings(real)
		t.Errorf("these v2 keys change nothing in the runtime config — the translator drops them, so "+
			"setting them in YAML does exactly nothing:\n  %s", strings.Join(real, "\n  "))
	}
}

// Every v2 field must carry a doc comment. CP39 lifts these verbatim into the generated reference,
// so an undocumented key would ship as a blank row — a documented key that says nothing is worse
// than an undocumented one, because it looks complete.
func TestEveryV2FieldIsDocumented(t *testing.T) {
	// The comment text itself is not visible through reflection, so this checks the property that
	// CP39's renderer will actually consume: the schema source file must carry a `//` line directly
	// above every YAML-tagged field. Parsing the file is what makes that checkable here.
	undocumented := undocumentedSchemaFields(t)
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Errorf("these v2 schema fields have no doc comment; CP39 renders them as blank reference "+
			"rows:\n  %s", strings.Join(undocumented, "\n  "))
	}
}

// enumProbeValues supplies an in-vocabulary value for enum string fields.
var enumProbeValues = map[string]string{
	// "auto" and anything unrecognised resolve from the jar path, so only an explicit mode moves the
	// runtime on an otherwise-empty schema.
	"indexer.java.mode": "advanced",
}

// setStringAt writes v into the string field at the given dotted YAML path.
func setStringAt(s *SchemaV2, path, v string) {
	cur := reflect.ValueOf(s).Elem()
	for _, seg := range strings.Split(path, ".") {
		rt := cur.Type()
		found := false
		for i := 0; i < rt.NumField(); i++ {
			if yamlFieldName(rt.Field(i)) == seg {
				cur = cur.Field(i)
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
	if cur.Kind() == reflect.String {
		cur.SetString(v)
	}
}

// forEachV2Leaf calls visit once per settable leaf field, with a setter that writes a distinctive
// non-zero value into a fresh schema.
func forEachV2Leaf(t *testing.T, visit func(path string, set func(*SchemaV2, bool))) {
	t.Helper()
	var walk func(rt reflect.Type, prefix string, index []int)
	walk = func(rt reflect.Type, prefix string, index []int) {
		for i := 0; i < rt.NumField(); i++ {
			ft := rt.Field(i)
			if ft.PkgPath != "" {
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
			idx := append(append([]int{}, index...), i)
			if ft.Type.Kind() == reflect.Struct {
				walk(ft.Type, path, idx)
				continue
			}
			p, at := path, idx
			visit(p, func(s *SchemaV2, probe bool) {
				setDistinctive(reflect.ValueOf(s).Elem().FieldByIndex(at), probe)
			})
		}
	}
	walk(reflect.TypeOf(SchemaV2{}), "", nil)
}

// setDistinctive writes a value that differs from the zero value for the field's kind. probe only
// affects pointer bools, which the caller tries both ways.
func setDistinctive(fv reflect.Value, probe bool) {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString("asqs-reachability-probe")
	case reflect.Bool:
		fv.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		fv.SetInt(4242)
	case reflect.Float32, reflect.Float64:
		fv.SetFloat(0.4242)
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.String {
			fv.Set(reflect.ValueOf([]string{"asqs-reachability-probe"}))
			return
		}
		fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
	case reflect.Map:
		m := reflect.MakeMap(fv.Type())
		m.SetMapIndex(reflect.ValueOf("asqs-reachability-probe"), reflect.New(fv.Type().Elem()).Elem())
		fv.Set(m)
	case reflect.Ptr:
		if fv.Type().Elem().Kind() == reflect.Bool {
			p := reflect.New(fv.Type().Elem())
			p.Elem().SetBool(probe)
			fv.Set(p)
		}
	}
}

// undocumentedSchemaFields parses schema_v2.go and reports "Type.Field" for every YAML-tagged field
// with no `//` comment line immediately above it.
func undocumentedSchemaFields(t *testing.T) []string {
	t.Helper()
	root := repoRootFromConfigPkg(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "internal", "config", "schema_v2.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse schema_v2.go: %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil || !strings.Contains(fld.Tag.Value, `yaml:"`) {
				continue
			}
			if fld.Doc != nil && len(fld.Doc.List) > 0 {
				continue
			}
			for _, nm := range fld.Names {
				out = append(out, ts.Name.Name+"."+nm.Name)
			}
		}
		return true
	})
	return out
}
