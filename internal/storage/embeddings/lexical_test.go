package embeddings

import (
	"os"
	"strings"
	"testing"
)

// The lexical channel is only useful if its query can actually match a document. Production
// synthesizes the query from a symbol, which yields one term per identifier plus one per camelCase
// part — for `OwnerController#processCreationForm` that is thirteen terms. Conjoining them (which
// is what plainto_tsquery does) requires all thirteen to co-occur in a single chunk.
//
// Measured against an indexed Spring PetClinic: the conjunctive form matched 0 of 387 chunks and
// the disjunctive form matched 119. A channel returning zero rows contributes nothing to the RRF
// fusion, so `fusion: rrf` behaved identically to `fusion: dense` — the A/B the mode exists to
// enable could not have shown a difference in either direction.
func TestOrTSQuery_disjoinsTerms(t *testing.T) {
	got := orTSQuery("processCreationForm process Creation Form OwnerController")
	want := "processCreationForm | process | Creation | Form | OwnerController"
	if got != want {
		t.Errorf("orTSQuery:\n got %q\nwant %q", got, want)
	}
}

// to_tsquery is a parser, not a plain-text entry point: an unescaped `&`, `|`, `!`, `(`, `)`, `:`
// or `'` in a term is a syntax error and fails the whole query. Terms arrive from FQNames and
// signature type names, which carry `.`, `#`, `<`, `>` and `[]`, so stripping is not optional.
func TestOrTSQuery_stripsQuerySyntax(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"com.acme.Order#place", "comacmeOrderplace"},
		{"List<Order>", "ListOrder"},
		{"a&b !c", "ab | c"},
		{"Order[]", "Order"},
	} {
		if got := orTSQuery(tc.in); got != tc.want {
			t.Errorf("orTSQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Duplicates are common because splitIdentifier emits the identifier and its parts, and a
// single-word identifier produces the same token twice. Repeating a lexeme in a tsquery is not an
// error but it inflates the term count for no benefit.
func TestOrTSQuery_dedupesCaseInsensitively(t *testing.T) {
	if got, want := orTSQuery("Owner owner OWNER Pet"), "Owner | Pet"; got != want {
		t.Errorf("orTSQuery = %q, want %q", got, want)
	}
}

// An empty or all-punctuation query must yield "" so SearchLexical returns early rather than
// sending to_tsquery('simple', ”) — which errors — or worse, a bare "|".
func TestOrTSQuery_emptyWhenNoUsableTerms(t *testing.T) {
	for _, in := range []string{"", "   ", "&&& |||", "."} {
		if got := orTSQuery(in); got != "" {
			t.Errorf("orTSQuery(%q) = %q, want empty", in, got)
		}
	}
}

// The lexical ORDER BY must be a total order. ts_rank_cd ties heavily — one measured query matched
// 74 chunks with 16 distinct scores — and ranks feed RRF, so an unordered tail makes `fusion: rrf`
// unreproducible. The golden suite scored nDCG@10 0.3968 and later 0.2543 with no code or corpus
// change before the tie-break was added.
//
// This asserts on the SQL text because the failure only manifests against a live server, which no
// unit test in this package reaches.
func TestSearchLexical_orderByIsTotal(t *testing.T) {
	b, err := os.ReadFile("lexical.go")
	if err != nil {
		t.Fatalf("read lexical.go: %v", err)
	}
	var code []string
	for _, ln := range strings.Split(string(b), "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		code = append(code, ln)
	}
	src := strings.Join(code, "\n")
	if !strings.Contains(src, "ORDER BY score DESC, file, start_line, id") {
		t.Error("SearchLexical must break ts_rank_cd ties on (file, start_line, id); " +
			"`ORDER BY score DESC` alone is not a total order and the ranking feeds RRF")
	}
}
