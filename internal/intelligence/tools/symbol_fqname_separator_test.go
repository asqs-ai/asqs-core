package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// The indexer joins FQName segments with '.', but a model that has just been reading repo-relative
// paths asks with the separator it read. Run api-3c56b784842358e936ec60e505209bc6 lost three
// get_symbol / expand_symbol calls to "src/app/core/session-context.service.SessionContextService"
// for a symbol that was indexed as "src.app.core.session-context.service.SessionContextService".
func TestResolveSymbol_pathSeparatorFallbackResolves(t *testing.T) {
	r, m, _ := testRegistry(t)
	const stored = "src.app.core.session-context.service.SessionContextService"
	sym := &metadata.Symbol{
		ID: "s-ctx", FQName: stored, File: "src/app/core/session-context.service.ts",
		Lang: "typescript", Kind: "class", StartLine: 3, EndLine: 10,
	}
	m.byFQ[stored] = []*metadata.Symbol{sym}

	for _, asked := range []string{
		"src/app/core/session-context.service.SessionContextService",
		`src\app\core\session-context.service.SessionContextService`,
	} {
		got, err := r.resolveSymbol(context.Background(), asked)
		if err != nil {
			t.Fatalf("resolveSymbol(%q): %v", asked, err)
		}
		if got.FQName != stored {
			t.Errorf("resolveSymbol(%q).FQName = %q, want %q", asked, got.FQName, stored)
		}
	}

	// expand_symbol shares resolveSymbol, so it must resolve the same spelling.
	if _, err := r.Invoke(context.Background(), ToolExpandSymbol,
		json.RawMessage(`{"fq_name":"src/app/core/session-context.service.SessionContextService","direction":"callers"}`)); err != nil {
		t.Errorf("expand_symbol: %v", err)
	}
}

// The fallback runs only after the exact lookup misses, which is what keeps it safe for the symbol
// kinds whose FQName legitimately carries a slash. Those must still resolve to themselves.
func TestResolveSymbol_slashBearingFQNamesStillResolveExactly(t *testing.T) {
	r, m, _ := testRegistry(t)
	for _, fq := range []string{
		"E2E_SPEC:e2e/smoke.spec.ts",
		"PAGE_ROUTE:/checkout@src.app.app.routes:L21",
	} {
		m.byFQ[fq] = []*metadata.Symbol{{ID: "s-" + fq, FQName: fq, File: "e2e/smoke.spec.ts", Lang: "typescript", Kind: "e2e_spec"}}
	}

	for fq := range m.byFQ {
		if !strings.ContainsAny(fq, `/\`) {
			continue
		}
		got, err := r.resolveSymbol(context.Background(), fq)
		if err != nil {
			t.Fatalf("resolveSymbol(%q): %v", fq, err)
		}
		if got.FQName != fq {
			t.Errorf("resolveSymbol(%q).FQName = %q, want the exact match", fq, got.FQName)
		}
	}
}

// A name with no separator, or one whose normalized form is also absent, keeps the original
// spelling in the error so an operator sees what the model actually asked for.
func TestResolveSymbol_missKeepsTheCallersSpelling(t *testing.T) {
	r, _, _ := testRegistry(t)

	_, err := r.resolveSymbol(context.Background(), "src/app/nowhere/Missing")
	if err == nil || !strings.Contains(err.Error(), `no symbol named "src/app/nowhere/Missing" is indexed`) {
		t.Fatalf("err = %v; want the miss to quote the caller's own spelling", err)
	}
}
