package retrieval

import (
	"reflect"
	"testing"
)

// ListGaps queries the symbol store by language. The store holds what the indexer wrote, and the
// C# indexer writes "csharp"; PlanOptions.Lang may carry either spelling. JS/TS already expanded
// both of its names for exactly this reason — C# did not, so a plan run with Lang "cs" queried a
// language no row has and selected nothing at all.
func TestSymbolQueryLangs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"csharp", []string{"csharp"}},
		{"cs", []string{"csharp"}},
		{"CS", []string{"csharp"}},
		{"C#", []string{"csharp"}},
		{"java", []string{"java"}},
		{"javascript", []string{"javascript", "typescript"}},
		{"typescript", []string{"javascript", "typescript"}},
		{"js", []string{"javascript", "typescript"}},
		{"ts", []string{"javascript", "typescript"}},
		{"", []string{""}},
	}
	for _, tc := range cases {
		if got := symbolQueryLangs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("symbolQueryLangs(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// gapSymbolKindsForLang keys off the same identifier and had the same exact-match problem: a JS
// alias reached the FUNCTION/METHOD/VARIABLE kinds, but nothing folded C#'s.
func TestGapSymbolKindsForLang_aliases(t *testing.T) {
	jsKinds := gapSymbolKindsForLang("javascript")
	for _, alias := range []string{"js", "ts", "jsx", "tsx", "typescript"} {
		if got := gapSymbolKindsForLang(alias); !reflect.DeepEqual(got, jsKinds) {
			t.Errorf("gapSymbolKindsForLang(%q) = %v, want %v", alias, got, jsKinds)
		}
	}
	for _, alias := range []string{"csharp", "cs", "java"} {
		if got := gapSymbolKindsForLang(alias); !reflect.DeepEqual(got, []string{"method"}) {
			t.Errorf("gapSymbolKindsForLang(%q) = %v, want [method]", alias, got)
		}
	}
}
