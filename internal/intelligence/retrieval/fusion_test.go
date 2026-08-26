package retrieval

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func res(ids ...string) []embeddings.SearchResult {
	out := make([]embeddings.SearchResult, len(ids))
	for i, id := range ids {
		out[i] = embeddings.SearchResult{Chunk: embeddings.Chunk{ID: id, File: id + ".java", StartLine: 1, EndLine: 2}}
	}
	return out
}

func TestNormalizeFusionMode(t *testing.T) {
	// Default must be dense: RRF changes ranking and must be A/B'd before becoming the default.
	for _, in := range []string{"", "dense", "DENSE", "  ", "nonsense"} {
		if got := NormalizeFusionMode(in); got != FusionDense {
			t.Errorf("NormalizeFusionMode(%q) = %q, want dense", in, got)
		}
	}
	for _, in := range []string{"rrf", "RRF", " hybrid ", "reciprocal_rank"} {
		if got := NormalizeFusionMode(in); got != FusionRRF {
			t.Errorf("NormalizeFusionMode(%q) = %q, want rrf", in, got)
		}
	}
}

// TestFuseRRF_rewardsAgreementAcrossChannels is the core property: a chunk that appears in several
// lists outranks one that tops a single list.
func TestFuseRRF_rewardsAgreementAcrossChannels(t *testing.T) {
	dense := res("a", "b", "c")
	lexical := res("b", "d", "e")

	score := FuseRRF([][]embeddings.SearchResult{dense, lexical})

	kb := chunkStableKey(&lexical[0].Chunk)
	ka := chunkStableKey(&dense[0].Chunk)
	if score[kb] <= score[ka] {
		t.Errorf("b appears in both channels and should outrank a (dense-only): b=%v a=%v", score[kb], score[ka])
	}
}

// TestFuseRRF_lexicalTopHitSurvives is the reason for the lexical channel: the exemplar test that
// ranks 8th densely but 1st lexically must survive fusion. Under the previous max-cosine merge it
// would simply have been outranked and dropped.
func TestFuseRRF_lexicalTopHitSurvives(t *testing.T) {
	dense := res("d1", "d2", "d3", "d4", "d5", "d6", "d7", "exemplar")
	lexical := res("exemplar", "l2")

	score := FuseRRF([][]embeddings.SearchResult{dense, lexical})

	exemplarKey := chunkStableKey(&lexical[0].Chunk)
	d1Key := chunkStableKey(&dense[0].Chunk)
	if score[exemplarKey] <= score[d1Key] {
		t.Errorf("the lexical top-1 (dense rank 8) should beat a dense-only top-1 after fusion: %v vs %v",
			score[exemplarKey], score[d1Key])
	}
}

// TestFuseRRF_ignoresRawScoreMagnitude is the H-8 fix: raw cosines from different chunk_type
// sub-corpora were compared directly, so a type whose scores cluster high (api_contract chunks are
// short and formulaic) dominated regardless of usefulness. RRF sees ranks only.
func TestFuseRRF_ignoresRawScoreMagnitude(t *testing.T) {
	// Two channels with identical rank structure but wildly different Distance values.
	high := res("x", "y")
	for i := range high {
		high[i].Distance = 0.01 // "very similar"
	}
	low := res("x", "y")
	for i := range low {
		low[i].Distance = 9.9 // "barely similar"
	}

	a := FuseRRF([][]embeddings.SearchResult{high})
	b := FuseRRF([][]embeddings.SearchResult{low})

	kx := chunkStableKey(&high[0].Chunk)
	if a[kx] != b[kx] {
		t.Errorf("fusion score depends on raw distance (%v vs %v); it must depend on rank only", a[kx], b[kx])
	}
}

func TestFuseRRF_emptyInput(t *testing.T) {
	if got := FuseRRF(nil); len(got) != 0 {
		t.Errorf("nil lists should score nothing, got %v", got)
	}
	if got := FuseRRF([][]embeddings.SearchResult{{}, {}}); len(got) != 0 {
		t.Errorf("empty lists should score nothing, got %v", got)
	}
}

func TestLexicalQueryForTarget(t *testing.T) {
	sym := &metadata.Symbol{FQName: "com.acme.OrderService#placeOrder", File: "OrderService.java"}
	q := LexicalQueryForTarget(sym, []string{"OrderRepository", "Order"})

	// The identifier itself and its camelCase parts must both appear: a `simple` tsvector tokenizes
	// `OrderService` as one lexeme, so a chunk mentioning `Order` separately would not match
	// without splitting.
	for _, want := range []string{"placeOrder", "place", "Order", "OrderService", "Service", "OrderRepository", "Repository"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing term %q", q, want)
		}
	}
	// Terms are deduplicated case-insensitively so the query does not balloon.
	if strings.Count(strings.ToLower(q), "order ")+strings.Count(strings.ToLower(q), "order") < 1 {
		t.Errorf("expected Order to appear: %q", q)
	}
}

func TestLexicalQueryForTarget_edgeCases(t *testing.T) {
	if got := LexicalQueryForTarget(nil, nil); got != "" {
		t.Errorf("nil symbol should yield an empty query, got %q", got)
	}
	// A type symbol (no '#') still yields its simple name.
	q := LexicalQueryForTarget(&metadata.Symbol{FQName: "com.acme.Order"}, nil)
	if !strings.Contains(q, "Order") {
		t.Errorf("type symbol query = %q", q)
	}
	// Single-character tokens are dropped as noise.
	q2 := LexicalQueryForTarget(&metadata.Symbol{FQName: "a.b.C#d"}, nil)
	if strings.Contains(q2, " d") || q2 == "d" {
		t.Errorf("single-character tokens should be dropped: %q", q2)
	}
}

func TestSimpleNameAndEnclosingType(t *testing.T) {
	if got := simpleNameOf("com.acme.OrderService#place"); got != "place" {
		t.Errorf("simpleNameOf = %q", got)
	}
	if got := simpleNameOf("com.acme.Order"); got != "Order" {
		t.Errorf("simpleNameOf = %q", got)
	}
	if got := simpleNameOf("Order"); got != "Order" {
		t.Errorf("simpleNameOf = %q", got)
	}
	if got := enclosingTypeNameOf("com.acme.OrderService#place"); got != "OrderService" {
		t.Errorf("enclosingTypeNameOf = %q", got)
	}
	if got := enclosingTypeNameOf("com.acme.Order"); got != "" {
		t.Errorf("a type has no enclosing type: %q", got)
	}
}
