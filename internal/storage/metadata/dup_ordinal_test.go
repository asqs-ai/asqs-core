package metadata

import (
	"reflect"
	"strings"
	"testing"
)

// dup_ordinal is what keeps two symbols the indexer cannot tell apart as SEPARATE stable rows. The
// advanced Java indexer emits `Type#method` for every overload, so without the ordinal a file's
// overloads collide on the natural key and the upsert silently merges them onto one row — losing
// every overload but one, invisibly.
func TestAssignDupOrdinals_numbersCollisionsByAppearance(t *testing.T) {
	syms := []*Symbol{
		{RepoID: "r", File: "A.java", FQName: "A#run", Kind: "method"},
		{RepoID: "r", File: "A.java", FQName: "A#other", Kind: "method"},
		{RepoID: "r", File: "A.java", FQName: "A#run", Kind: "method"},
		{RepoID: "r", File: "A.java", FQName: "A#run", Kind: "method"},
	}
	if got, want := assignDupOrdinals(syms), []int{1, 1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("ordinals = %v, want %v", got, want)
	}
}

// Each part of the natural key must separate: same name in a different file, or a different kind,
// is a different symbol and must start over at 1.
func TestAssignDupOrdinals_keyPartsSeparate(t *testing.T) {
	syms := []*Symbol{
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "method"},
		{RepoID: "r", File: "B.java", FQName: "X", Kind: "method"}, // different file
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "class"},  // different kind
		{RepoID: "s", File: "A.java", FQName: "X", Kind: "method"}, // different repo
	}
	for i, got := range assignDupOrdinals(syms) {
		if got != 1 {
			t.Errorf("symbol %d got ordinal %d; each differs in one key part and must start at 1", i, got)
		}
	}
}

// InsertSymbol lowercases kind on the way in, so the ordinal key must lowercase it too. Comparing
// the raw value would give "Method" and "method" separate ordinals that then collide in the
// database, where both are stored as "method" — a unique-violation on an ordinary reindex.
func TestAssignDupOrdinals_kindIsCaseInsensitive(t *testing.T) {
	syms := []*Symbol{
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "method"},
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "Method"},
		{RepoID: "r", File: "A.java", FQName: "X", Kind: " METHOD "},
	}
	if got, want := assignDupOrdinals(syms), []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("ordinals = %v, want %v — kind must be compared the way it is stored", got, want)
	}
}

// A nil entry must not shift the ordinals of everything after it: InsertSymbols indexes the returned
// slice positionally, so a shift would bind each symbol to the wrong ordinal.
func TestAssignDupOrdinals_nilDoesNotShift(t *testing.T) {
	syms := []*Symbol{
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "method"},
		nil,
		{RepoID: "r", File: "A.java", FQName: "X", Kind: "method"},
	}
	got := assignDupOrdinals(syms)
	if len(got) != 3 || got[0] != 1 || got[2] != 2 {
		t.Errorf("ordinals = %v, want position 0 -> 1 and position 2 -> 2", got)
	}
}

// The insert must be an UPSERT on the natural key. A plain insert would mint a new id on every
// reindex, which is the entire condition CP13 exists to remove: chunks.symbol_id would dangle and
// symbol_versions would scatter one symbol's history across a new row per run.
func TestSymbolInsertQuery_isAnUpsertOnTheNaturalKey(t *testing.T) {
	if !strings.Contains(symbolInsertQuery, "ON CONFLICT (repo_id, file, fq_name, kind, dup_ordinal)") {
		t.Error("symbolInsertQuery does not upsert on the natural key; symbol ids will not survive a reindex")
	}
	if !strings.Contains(symbolInsertQuery, "DO UPDATE SET") {
		t.Error("the conflict arm does not update, so a changed symbol keeps stale line numbers")
	}
	// The conflict target must not appear in the SET list: assigning it is a no-op that reads as
	// though a row could move between natural keys.
	for _, col := range []string{"repo_id = EXCLUDED", "file = EXCLUDED", "fq_name = EXCLUDED", "kind = EXCLUDED"} {
		if strings.Contains(symbolInsertQuery, col) {
			t.Errorf("the SET list assigns %q, which is part of the conflict target", col)
		}
	}
	// And the mutable columns must all be there, or a reindex leaves stale data behind.
	for _, col := range []string{"start_line", "end_line", "signature_json", "lang"} {
		if !strings.Contains(symbolInsertQuery, col+" = EXCLUDED."+col) {
			t.Errorf("the SET list omits %s; a reindex would leave it stale", col)
		}
	}
}
