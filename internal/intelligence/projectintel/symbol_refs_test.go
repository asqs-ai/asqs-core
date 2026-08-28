package projectintel

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// ── ExtractSymbolRefs ─────────────────────────────────────────────────────────

func TestExtractSymbolRefs_CapitalizedTokens(t *testing.T) {
	refs := ExtractSymbolRefs("Use PaymentGateway to process all charges.")
	if !sliceContains(refs, "PaymentGateway") {
		t.Fatalf("expected PaymentGateway, got %v", refs)
	}
}

func TestExtractSymbolRefs_SkipsCommonTypes(t *testing.T) {
	refs := ExtractSymbolRefs("Use String, List, Map, Object, Exception.")
	for _, r := range refs {
		if commonDocNoiseNames[r] {
			t.Fatalf("expected common type %q to be filtered, but got it in refs %v", r, refs)
		}
	}
}

func TestExtractSymbolRefs_FQNames(t *testing.T) {
	// Dotted names starting with an uppercase segment are captured whole.
	refs := ExtractSymbolRefs("Use Example.PaymentGateway for charges.")
	found := false
	for _, r := range refs {
		if r == "Example.PaymentGateway" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Example.PaymentGateway in refs, got %v", refs)
	}
}

func TestExtractSymbolRefs_LowercaseFQLeadExtractsTrailing(t *testing.T) {
	// com.example.PaymentGateway: "com" is lowercase so regex only matches PaymentGateway.
	refs := ExtractSymbolRefs("Use com.example.PaymentGateway for charges.")
	if !sliceContains(refs, "PaymentGateway") {
		t.Fatalf("expected at least PaymentGateway from FQ name, got %v", refs)
	}
}

func TestExtractSymbolRefs_EmptyContent(t *testing.T) {
	if refs := ExtractSymbolRefs(""); len(refs) != 0 {
		t.Fatalf("expected nil refs for empty content, got %v", refs)
	}
}

func TestExtractSymbolRefs_NoDuplicates(t *testing.T) {
	refs := ExtractSymbolRefs("Use PaymentGateway here and PaymentGateway there.")
	count := 0
	for _, r := range refs {
		if r == "PaymentGateway" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 PaymentGateway, got %d in %v", count, refs)
	}
}

// ── ResolveDocSymbolLinks ─────────────────────────────────────────────────────

type fakeSymbolResolver struct {
	byFQ map[string][]*metadata.Symbol
}

func (f *fakeSymbolResolver) ListSymbolsByFQName(_ context.Context, _, fq string) ([]*metadata.Symbol, error) {
	return f.byFQ[fq], nil
}

func TestResolveDocSymbolLinks_ResolvesKnown(t *testing.T) {
	resolver := &fakeSymbolResolver{byFQ: map[string][]*metadata.Symbol{
		"PaymentGateway": {{ID: "sym-1", FQName: "PaymentGateway"}},
	}}
	ids := ResolveDocSymbolLinks(context.Background(), "", []string{"PaymentGateway"}, resolver)
	if len(ids) != 1 || ids[0] != "sym-1" {
		t.Fatalf("expected [sym-1], got %v", ids)
	}
}

func TestResolveDocSymbolLinks_UnknownDropped(t *testing.T) {
	resolver := &fakeSymbolResolver{byFQ: map[string][]*metadata.Symbol{}}
	ids := ResolveDocSymbolLinks(context.Background(), "", []string{"NonExistentType"}, resolver)
	if len(ids) != 0 {
		t.Fatalf("expected empty ids for unknown type, got %v", ids)
	}
}

func TestResolveDocSymbolLinks_NilResolver(t *testing.T) {
	ids := ResolveDocSymbolLinks(context.Background(), "", []string{"PaymentGateway"}, nil)
	if ids != nil {
		t.Fatalf("expected nil when resolver is nil, got %v", ids)
	}
}

func TestResolveDocSymbolLinks_DeduplicatesSymbolIDs(t *testing.T) {
	sym := &metadata.Symbol{ID: "sym-1", FQName: "PaymentGateway"}
	resolver := &fakeSymbolResolver{byFQ: map[string][]*metadata.Symbol{
		"PaymentGateway":             {sym},
		"com.example.PaymentGateway": {sym}, // same ID via different ref
	}}
	ids := ResolveDocSymbolLinks(context.Background(), "", []string{"PaymentGateway", "com.example.PaymentGateway"}, resolver)
	count := 0
	for _, id := range ids {
		if id == "sym-1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected sym-1 exactly once, got %d in %v", count, ids)
	}
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
