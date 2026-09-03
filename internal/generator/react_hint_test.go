package generator

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// F8. A component using react-router must be rendered inside a router, not have the router
// mocked away — `<Link>` outside a router is what failed HomePage.test.tsx in the asqs-go run of
// 2026-09-03. The hint applies to .tsx targets and to RENDERS-kind symbols, and not to plain .ts.
func TestReactTSXUnitTestHint_namesMemoryRouter(t *testing.T) {
	tsx := &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "FUNCTION", File: "src/pages/HomePage.tsx"}},
	}
	got := reactTSXUnitTestHint(tsx)
	if !strings.Contains(got, "@testing-library/react") || !strings.Contains(got, "MemoryRouter") || !strings.Contains(got, "rather than mocking `react-router-dom`") {
		t.Fatalf("React hint must tell the model to wrap router components in MemoryRouter; got %q", got)
	}
	plain := &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{Lang: "typescript", Kind: "FUNCTION", File: "src/lib/pure.ts"}},
	}
	if got := reactTSXUnitTestHint(plain); got != "" {
		t.Fatalf("plain .ts non-UI kind must not get the React hint; got %q", got)
	}
}
