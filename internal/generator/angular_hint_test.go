package generator

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// Run 2026-09-03 (Angular fixture): LegacyPortalComponent is `standalone: false` and was put in
// `imports:`; CatalogService injects the API_BASE_URL token and no provider was given. Both wiring
// rules are stated once for every Angular-shaped target, and not for React or plain TS files.
func TestAngularUnitTestHint_appliesToAngularFilesOnly(t *testing.T) {
	component := &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "method", File: "src/app/legacy/legacy-portal.component.ts"}},
	}
	got := angularUnitTestHint(component)
	for _, want := range []string{"standalone: false", "declarations: [X]", "Unexpected directive", "InjectionToken", "provideHttpClientTesting", "'constructor'", "jasmine is not defined", "jest.fn()"} {
		if !strings.Contains(got, want) {
			t.Errorf("Angular hint lacks %q; got %q", want, got)
		}
	}
	byKind := &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "angular_service", File: "src/app/core/tokens.ts"}},
	}
	if angularUnitTestHint(byKind) == "" {
		t.Error("an angular_* symbol kind must get the hint whatever the file is called")
	}
	for name, item := range map[string]*retrieval.TestPlanItem{
		"react tsx": {Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "FUNCTION", File: "src/pages/HomePage.tsx"}}},
		"plain ts":  {Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "FUNCTION", File: "src/lib/pure.ts"}}},
		"java":      {Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "java", Kind: "method", File: "src/main/java/a/OwnerService.java"}}},
	} {
		if got := angularUnitTestHint(item); got != "" {
			t.Errorf("%s must not get the Angular hint; got %q", name, got)
		}
	}
	if angularUnitTestHint(nil) != "" || angularUnitTestHint(&retrieval.TestPlanItem{}) != "" {
		t.Error("nil-safe")
	}
}
