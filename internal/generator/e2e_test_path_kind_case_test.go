package generator

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func pageRouteItem(kind string) *retrieval.TestPlanItem {
	return &retrieval.TestPlanItem{
		Layer: "e2e",
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Kind:   kind,
			Lang:   "typescript",
			FQName: "PAGE_ROUTE:/catalog@src.app.app.routes:L16",
			File:   "src/app/app.routes.ts",
		}},
	}
}

// The regression for run 2026-09-01T08:36Z. metadata.InsertSymbol lowercases every kind, so a symbol
// read back from storage carries `page_route` — and SuggestedTestPath switched on the SCREAMING_CASE
// literal. Both PAGE_ROUTE gaps fell through to the generic branch, were given the UNIT test path for
// src/app/app.routes.ts, found the unit suite on disk and extended it with a Playwright payload.
func TestSuggestedTestPath_pageRouteKindIsCaseInsensitive(t *testing.T) {
	for _, kind := range []string{"page_route", "PAGE_ROUTE", "  Page_Route  "} {
		got := SuggestedTestPath(pageRouteItem(kind), "jest", "playwright", "")
		if !strings.HasPrefix(got, "e2e/") {
			t.Errorf("kind %q → %q, want an e2e/ path; a unit path here is how a Playwright spec reached a Jest suite", kind, got)
		}
		if strings.Contains(got, ".test.ts") {
			t.Errorf("kind %q → %q, which is the unit-test path for the source file", kind, got)
		}
	}
}

// The same mismatch made every other E2E kind fall through.
func TestSuggestedTestPath_lowercaseE2EKinds(t *testing.T) {
	spec := &retrieval.TestPlanItem{Layer: "e2e", Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
		Kind: "e2e_spec", Lang: "typescript", FQName: "E2E_SPEC:e2e/smoke.spec.ts", File: "e2e/smoke.spec.ts",
	}}}
	if got := SuggestedTestPath(spec, "jest", "playwright", ""); got != "e2e/smoke.spec.ts" {
		t.Errorf("e2e_spec → %q, want the spec's own file", got)
	}
	api := &retrieval.TestPlanItem{Layer: "e2e", Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
		Kind: "api_route", Lang: "typescript", FQName: "API_ROUTE:GET /orders@src/api.ts:L3", File: "src/api.ts",
	}}}
	got := SuggestedTestPath(api, "jest", "playwright", "")
	if strings.Contains(got, ".test.ts") {
		t.Errorf("api_route → %q, which is a unit path", got)
	}
}

// A genuine unit gap must keep its unit path: the fix must not send everything to e2e/.
func TestSuggestedTestPath_unitKindsUnaffected(t *testing.T) {
	unit := &retrieval.TestPlanItem{Layer: "unit", Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
		Kind: "variable", Lang: "typescript", FQName: "src.app.app.routes.APP_ROUTES", File: "src/app/app.routes.ts",
	}}}
	got := SuggestedTestPath(unit, "jest", "playwright", "")
	if !strings.Contains(got, "app.routes") || strings.HasPrefix(got, "e2e/") {
		t.Errorf("unit gap → %q, want the source file's unit test path", got)
	}
}

// IsE2EPlanItem must not depend on Layer alone: an item whose kind says e2e is an e2e item even when
// the layer field is empty, which is what the path suggester and the layer gate both rely on.
func TestIsE2EPlanItem_lowercaseKindWithoutLayer(t *testing.T) {
	item := pageRouteItem("page_route")
	item.Layer = ""
	if !retrieval.IsE2EPlanItem(item) {
		t.Error("a page_route symbol is an E2E item regardless of the Layer field")
	}
}

func TestSymbolKindIs(t *testing.T) {
	cases := map[string]bool{"page_route": true, "PAGE_ROUTE": true, "  page_route ": true, "pageroute": false, "": false}
	for in, want := range cases {
		if got := retrieval.SymbolKindIs(in, "PAGE_ROUTE"); got != want {
			t.Errorf("SymbolKindIs(%q) = %v, want %v", in, got, want)
		}
	}
}
