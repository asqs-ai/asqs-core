package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func scratchStoreForBatchTest(t *testing.T) (*Store, context.Context) {
	t.Helper()
	conn, why := ScratchDBForTests()
	if conn == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

// A failing batch must leave NOTHING behind.
//
// This is the review focus for B28, and the reason it matters is the call order rather than the
// batch itself: the indexer deletes a file's existing symbols and then re-inserts them. A batch that
// applied a prefix would leave the file with fewer symbols than it started with — no error surfaced
// to a human, no count anyone checks, just retrieval quality that quietly dropped for that file
// until someone reindexed.
func TestInsertSymbols_partialFailureLeavesNothing(t *testing.T) {
	s, ctx := scratchStoreForBatchTest(t)
	repo := fmt.Sprintf("batch/atomic-%d", time.Now().UnixNano())
	file := "src/main/java/p/Batch.java"
	t.Cleanup(func() {
		_, _ = s.DeleteSymbolsByFile(context.Background(), repo, file)
	})

	mk := func(fq string, sig []byte) *Symbol {
		return &Symbol{
			Lang: "java", Kind: "class", FQName: fq, File: file,
			StartLine: 1, EndLine: 2, RepoID: repo, SignatureJSON: sig,
		}
	}
	// The middle row carries malformed JSON for a JSONB column, so the server rejects that
	// statement and only that statement.
	syms := []*Symbol{
		mk("p.First", nil),
		mk("p.Broken", []byte("{this is not json")),
		mk("p.Third", nil),
	}

	ids, err := s.InsertSymbols(ctx, syms)
	if err == nil {
		t.Fatal("expected the batch to fail on the malformed row")
	}
	if len(ids) != 0 {
		t.Errorf("InsertSymbols returned %d ids alongside an error; callers index those ids into "+
			"their fq_name map and would build edges pointing at rows that were rolled back", len(ids))
	}

	got, err := s.ListSymbolsByFile(ctx, repo, file)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		names := make([]string, 0, len(got))
		for _, g := range got {
			names = append(names, g.FQName)
		}
		t.Errorf("a failed batch left %d symbol(s) behind (%v); the file is now half-indexed and "+
			"nothing reports it", len(got), names)
	}
}

// The happy path: ids come back in input order, because the caller maps them onto its own slice by
// index to resolve the file's edges. Order drift here would bind edges to the wrong symbols — a
// corruption that no error surfaces and no count reveals.
func TestInsertSymbols_returnsIdsInInputOrder(t *testing.T) {
	s, ctx := scratchStoreForBatchTest(t)
	repo := fmt.Sprintf("batch/order-%d", time.Now().UnixNano())
	file := "src/main/java/p/Order.java"
	t.Cleanup(func() {
		_, _ = s.DeleteSymbolsByFile(context.Background(), repo, file)
	})

	want := []string{"p.Alpha", "p.Beta", "p.Gamma", "p.Delta"}
	syms := make([]*Symbol, 0, len(want))
	for i, fq := range want {
		syms = append(syms, &Symbol{
			Lang: "java", Kind: "class", FQName: fq, File: file,
			StartLine: i + 1, EndLine: i + 1, RepoID: repo,
		})
	}
	ids, err := s.InsertSymbols(ctx, syms)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(want) {
		t.Fatalf("got %d ids for %d symbols", len(ids), len(want))
	}
	for i, id := range ids {
		got, err := s.GetSymbolByID(ctx, repo, id)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("id %d (%s) does not resolve", i, id)
		}
		if got.FQName != want[i] {
			t.Errorf("ids are out of order: position %d is %q, want %q — edges built from this "+
				"mapping would bind to the wrong symbols", i, got.FQName, want[i])
		}
	}
}

// InsertEdges is idempotent and repairs repo_id, matching InsertEdge exactly.
func TestInsertEdges_isIdempotent(t *testing.T) {
	s, ctx := scratchStoreForBatchTest(t)
	repo := fmt.Sprintf("batch/edges-%d", time.Now().UnixNano())
	file := "src/main/java/p/Edges.java"
	t.Cleanup(func() {
		_, _ = s.DeleteSymbolsByFile(context.Background(), repo, file)
	})

	ids, err := s.InsertSymbols(ctx, []*Symbol{
		{Lang: "java", Kind: "class", FQName: "p.A", File: file, StartLine: 1, EndLine: 1, RepoID: repo},
		{Lang: "java", Kind: "method", FQName: "p.A#b", File: file, StartLine: 2, EndLine: 2, RepoID: repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	edges := []*Edge{{CallerSymbolID: ids[0], CalleeSymbolID: ids[1], EdgeType: "CONTAINS", RepoID: repo}}

	for i := 0; i < 2; i++ {
		if err := s.InsertEdges(ctx, edges); err != nil {
			t.Fatalf("InsertEdges pass %d: %v", i+1, err)
		}
	}
	got, err := s.GetEdgesFrom(ctx, repo, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("got %d edges after inserting the same edge twice, want 1", len(got))
	}
}

// ListSymbolsByFQNames must answer exactly as a sequence of ListSymbolsByFQName calls would,
// including row order within a name — the indexer's disambiguation takes syms[0] when it has no
// file hint, so a different order is a different edge.
func TestListSymbolsByFQNames_matchesPerNameLookups(t *testing.T) {
	s, ctx := scratchStoreForBatchTest(t)
	repo := fmt.Sprintf("batch/fqnames-%d", time.Now().UnixNano())
	fileA := "src/main/java/a/Dup.java"
	fileB := "src/main/java/b/Dup.java"
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.DeleteSymbolsByFile(bg, repo, fileA)
		_, _ = s.DeleteSymbolsByFile(bg, repo, fileB)
	})

	// The same FQName in two files, which is what makes order observable.
	for _, f := range []string{fileB, fileA} { // inserted in reverse, so order cannot come from insertion
		if _, err := s.InsertSymbols(ctx, []*Symbol{
			{Lang: "java", Kind: "class", FQName: "p.Dup", File: f, StartLine: 1, EndLine: 1, RepoID: repo},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.InsertSymbols(ctx, []*Symbol{
		{Lang: "java", Kind: "class", FQName: "p.Solo", File: fileA, StartLine: 5, EndLine: 5, RepoID: repo},
	}); err != nil {
		t.Fatal(err)
	}

	names := []string{"p.Dup", "p.Solo", "p.NeverIndexed"}
	batched, err := s.ListSymbolsByFQNames(ctx, repo, names)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		want, err := s.ListSymbolsByFQName(ctx, repo, n)
		if err != nil {
			t.Fatal(err)
		}
		got := batched[n]
		if len(got) != len(want) {
			t.Errorf("%s: batched returned %d rows, per-name returned %d", n, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				t.Errorf("%s: row %d differs (batched %s@%s, per-name %s@%s); the indexer takes "+
					"the first row when it has no file hint, so this changes which edge is written",
					n, i, got[i].FQName, got[i].File, want[i].FQName, want[i].File)
			}
		}
	}
	if _, present := batched["p.NeverIndexed"]; present {
		t.Error("an unindexed name must be absent from the map, not present with zero rows")
	}
}
