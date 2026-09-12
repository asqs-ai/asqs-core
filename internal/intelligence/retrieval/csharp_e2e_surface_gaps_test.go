package retrieval

import (
	"reflect"
	"testing"
)

// A C# repository with pages a browser can drive has uncovered PAGE_ROUTEs, exactly the way a React
// one does. ListGapsE2E queried only E2E_SPEC for C#, so a Razor Pages application's E2E plan could
// only ever anchor on API routes — the pages themselves were invisible to it.
func TestE2ESymbolQueriesForWorkflowLang_csharpBySurface(t *testing.T) {
	kindsFor := func(surface string) []string {
		var out []string
		for _, q := range e2eSymbolQueriesForWorkflowLangSurface("csharp", surface) {
			if q.lang != "csharp" {
				t.Fatalf("unexpected language %q in a C# query set", q.lang)
			}
			out = append(out, q.kind)
		}
		return out
	}

	// An API surface keeps today's behaviour exactly.
	if got, want := kindsFor("api"), []string{"E2E_SPEC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("api surface kinds = %v, want %v", got, want)
	}
	// So does an undetected one.
	if got, want := kindsFor(""), []string{"E2E_SPEC"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unknown surface kinds = %v, want %v", got, want)
	}
	// A UI surface adds the page-shaped anchors Java already has.
	for _, surface := range []string{"ui", "mixed"} {
		got := kindsFor(surface)
		for _, want := range []string{"E2E_SPEC", "PAGE_OBJECT", "USER_FLOW"} {
			found := false
			for _, k := range got {
				if k == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s surface kinds = %v, missing %q", surface, got, want)
			}
		}
	}
}

// Java and JS/TS are unchanged by the surface parameter.
func TestE2ESymbolQueriesForWorkflowLang_otherLanguagesUnchanged(t *testing.T) {
	for _, lang := range []string{"java", "typescript", "javascript"} {
		for _, surface := range []string{"", "ui", "api", "mixed"} {
			got := e2eSymbolQueriesForWorkflowLangSurface(lang, surface)
			want := e2eSymbolQueriesForWorkflowLang(lang)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s with surface %q = %v, want %v", lang, surface, got, want)
			}
		}
	}
}

// PAGE_ROUTE anchors from non-test C# files are listed for a UI surface and not for an API one.
func TestPageRouteE2EGapLangs_csharpSurface(t *testing.T) {
	cases := []struct {
		surface string
		want    []string
	}{
		{"ui", []string{"csharp"}},
		{"mixed", []string{"csharp"}},
		{"api", nil},
		{"none", nil},
		{"", nil},
	}
	for _, tc := range cases {
		opts := PlanOptions{Lang: "csharp", E2ESurface: tc.surface}
		if got := pageRouteE2EGapLangsForSurface(opts); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("surface %q: page-route langs = %v, want %v", tc.surface, got, tc.want)
		}
	}
	// The cs alias behaves the same.
	if got := pageRouteE2EGapLangsForSurface(PlanOptions{Lang: "cs", E2ESurface: "ui"}); !reflect.DeepEqual(got, []string{"csharp"}) {
		t.Errorf("cs alias: page-route langs = %v, want [csharp]", got)
	}
	// A Java repo never gets C# page routes.
	if got := pageRouteE2EGapLangsForSurface(PlanOptions{Lang: "java", E2ESurface: "ui"}); got != nil {
		t.Errorf("java: page-route langs = %v, want none", got)
	}
}
