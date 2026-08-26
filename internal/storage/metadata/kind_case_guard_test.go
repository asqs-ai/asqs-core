package metadata

import (
	"os"
	"strings"
	"testing"
)

// Write/read symmetry for symbol `kind`, asserted on source so CI catches it without a database.
//
// InsertSymbol lowercases kind. Any query comparing kind exactly must therefore normalize its
// argument, or SCREAMING_CASE callers silently match nothing. That is what stopped E2E test
// generation entirely: ListGapsE2E asks for "API_ROUTE" and got zero rows forever.
func TestSymbolKindComparisonsAreNormalized(t *testing.T) {
	// store.go AND batch.go: the INSERT's argument normalization moved to batch.go when the
	// single-row and batched paths were made to share one query and one arg builder. Reading only
	// store.go would leave this guard asserting nothing.
	src := readMetadataSourceWithoutComments(t, "store.go", "batch.go")

	if !strings.Contains(src, "func normalizeSymbolKind(") {
		t.Fatal("normalizeSymbolKind is gone; kind comparisons are no longer symmetric with InsertSymbol")
	}
	if !strings.Contains(src, "strings.ToLower(strings.TrimSpace(sym.Kind))") {
		t.Fatal("InsertSymbol no longer lowercases kind; this guard is checking the wrong invariant")
	}

	// Every query that compares `kind` exactly must pass a normalized argument.
	exact := strings.Count(src, "kind = $2")
	normalized := strings.Count(src, "normalizeSymbolKind(kind)")
	if exact > normalized {
		t.Errorf("%d quer(ies) compare kind exactly but only %d normalize the argument; "+
			"a SCREAMING_CASE kind such as API_ROUTE or E2E_SPEC will match nothing", exact, normalized)
	}
}

// The E2E kind constants must stay uppercase at the call sites — that is the codebase's convention
// and the indexer emits them that way. The fix belongs in the store, not in rewriting every literal;
// this pins which side owns the normalization so a future "fix" does not move it back and reopen the
// gap for callers that are missed.
func TestE2EKindLiteralsRemainAtCallSites(t *testing.T) {
	b, err := os.ReadFile("../../intelligence/retrieval/plan.go")
	if err != nil {
		t.Skipf("plan.go not readable from here: %v", err)
	}
	src := string(b)
	for _, kind := range []string{"API_ROUTE", "E2E_SPEC", "PAGE_ROUTE"} {
		if !strings.Contains(src, `"`+kind+`"`) {
			t.Errorf("%s literal vanished from plan.go; if kinds moved to lowercase at call sites, "+
				"the store-side normalization must still cover callers that were not updated", kind)
		}
	}
}
