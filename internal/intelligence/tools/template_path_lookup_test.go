package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// The E2E selector inventory lists template paths as headings, and a model reading it reaches for
// the template the obvious way. Run api-c81d90a22d1460d87b64e483837fdc24 lost seven get_symbol /
// find_tests_for lookups to catalog/checkout/shell `.component.html`. Telling the model not to ask
// was tried in the inventory text and did not work — its instinct is right, the index HAS those
// templates under a different name.
func TestResolveSymbol_templatePathResolvesToTheIndexedTemplate(t *testing.T) {
	r, m, _ := testRegistry(t)
	const path = "src/app/features/catalog/catalog.component.html"
	const stored = "STATIC_TEMPLATE:" + path
	m.byFQ[stored] = []*metadata.Symbol{{
		ID: "s-tpl", FQName: stored, File: path, Lang: "html", Kind: "static_template", StartLine: 1, EndLine: 26,
	}}

	got, err := r.resolveSymbol(context.Background(), path)
	if err != nil {
		t.Fatalf("resolveSymbol(%q): %v", path, err)
	}
	if got.FQName != stored {
		t.Errorf("FQName = %q, want %q", got.FQName, stored)
	}
}

// The Angular component enricher keys its own stub on the component-relative templateUrl, so only
// the base name survives a repo-relative lookup. It is the second candidate because STATIC_TEMPLATE
// owns the UI_TEST_HOOK children the caller is actually after.
func TestResolveSymbol_templatePathFallsBackToTheAngularStub(t *testing.T) {
	r, m, _ := testRegistry(t)
	const stub = "ANGULAR_TEMPLATE:./catalog.component.html"
	m.byFQ[stub] = []*metadata.Symbol{{
		ID: "s-ng", FQName: stub, File: "src/app/features/catalog/catalog.component.ts", Lang: "typescript", Kind: "angular_template",
	}}

	got, err := r.resolveSymbol(context.Background(), "src/app/features/catalog/catalog.component.html")
	if err != nil {
		t.Fatalf("resolveSymbol: %v", err)
	}
	if got.FQName != stub {
		t.Errorf("FQName = %q, want %q", got.FQName, stub)
	}
}

// expand_symbol shares resolveSymbol, so the same spelling must work there.
func TestExpandSymbol_acceptsATemplatePath(t *testing.T) {
	r, m, _ := testRegistry(t)
	const stored = "STATIC_TEMPLATE:src/app/layout/shell.component.html"
	m.byFQ[stored] = []*metadata.Symbol{{ID: "s-shell", FQName: stored, File: "src/app/layout/shell.component.html", Lang: "html", Kind: "static_template"}}

	if _, err := r.Invoke(context.Background(), ToolExpandSymbol,
		json.RawMessage(`{"fq_name":"src/app/layout/shell.component.html","direction":"callees"}`)); err != nil {
		t.Errorf("expand_symbol: %v", err)
	}
}

// A path that is not a template must not gain extra lookups, and a template that genuinely is not
// indexed must still miss with the caller's own spelling.
func TestResolveSymbol_templateRungIsNarrow(t *testing.T) {
	r, _, _ := testRegistry(t)

	if _, err := r.resolveSymbol(context.Background(), "src/app/features/catalog/catalog.component.html"); err == nil {
		t.Error("an unindexed template must still miss")
	} else if !strings.Contains(err.Error(), "catalog.component.html") {
		t.Errorf("miss must quote the caller's spelling, got: %v", err)
	}
	// Not a template path: the rung must not fire.
	if got := templateSymbolCandidates("src/app/features/catalog/catalog.component.ts"); got != nil {
		t.Errorf("templateSymbolCandidates fired for a .ts file: %v", got)
	}
	if got := templateSymbolCandidates("com.acme.Thing"); got != nil {
		t.Errorf("templateSymbolCandidates fired for a dotted FQName: %v", got)
	}
	// .htm counts, and both candidates are offered in the documented order.
	got := templateSymbolCandidates("src/app/x.component.htm")
	want := []string{"STATIC_TEMPLATE:src/app/x.component.htm", "ANGULAR_TEMPLATE:./x.component.htm"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
