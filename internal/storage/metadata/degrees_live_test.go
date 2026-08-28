package metadata

import (
	"context"
	"testing"
)

// TestRecomputeSymbolDegrees is a live-database test because the whole point of the feature is a SQL
// aggregate replacing per-symbol round trips; an in-memory fake would test nothing.
func TestRecomputeSymbolDegrees(t *testing.T) {
	url, why := ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	const repo, file = "deg/repo", "src/Deg.java"
	cleanup := func() {
		_, _ = st.DeleteSymbolsByFile(ctx, repo, file)
		_, _ = st.DeleteFile(ctx, repo, file)
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := st.UpsertFile(ctx, &File{File: file, SHA: "s", Lang: "java"}); err != nil {
		t.Fatal(err)
	}

	mk := func(fq string) string {
		id, err := st.InsertSymbol(ctx, &Symbol{
			Lang: "java", Kind: "method", FQName: fq, File: file, StartLine: 1, EndLine: 2, RepoID: repo,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// target is called by two production symbols and covered by one test.
	target := mk("com.acme.Target#run")
	callerA := mk("com.acme.A#a")
	callerB := mk("com.acme.B#b")
	testSym := mk("com.acme.TargetTest#t")

	// Every edge carries the repo: degree recompute is repo-scoped, so an unscoped edge would be
	// counted for nobody and the degrees would silently come back zero.
	for _, e := range []*Edge{
		{CallerSymbolID: callerA, CalleeSymbolID: target, EdgeType: "CALLS", RepoID: repo},
		{CallerSymbolID: callerB, CalleeSymbolID: target, EdgeType: "CALLS", RepoID: repo},
		{CallerSymbolID: testSym, CalleeSymbolID: target, EdgeType: EdgeTypeTestsSource, RepoID: repo},
	} {
		if err := st.InsertEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.RecomputeSymbolDegrees(ctx, repo); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetSymbolByID(ctx, repo, target)
	if err != nil || got == nil {
		t.Fatalf("GetSymbolByID: %v", err)
	}
	if got.InDegree != 3 {
		t.Errorf("InDegree = %d, want 3 (two calls + one test edge)", got.InDegree)
	}
	// The distinction the centrality signal depends on.
	if got.InDegreeNonTest != 2 {
		t.Errorf("InDegreeNonTest = %d, want 2 — TESTS_SOURCE must not count, or a well-tested "+
			"symbol looks like a central under-tested one", got.InDegreeNonTest)
	}
	if got.OutDegree != 0 {
		t.Errorf("OutDegree = %d, want 0", got.OutDegree)
	}

	caller, err := st.GetSymbolByID(ctx, repo, callerA)
	if err != nil || caller == nil {
		t.Fatalf("GetSymbolByID: %v", err)
	}
	if caller.OutDegree != 1 || caller.InDegree != 0 {
		t.Errorf("caller degrees = in %d / out %d, want 0 / 1", caller.InDegree, caller.OutDegree)
	}
}
