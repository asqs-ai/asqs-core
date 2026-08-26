package metadata

import (
	"context"
	"testing"
)

// TestSymbolKindLookupIsCaseInsensitive is the regression that stopped E2E generation.
//
// InsertSymbol lowercases `kind` at write time so gap queries can compare directly and use the
// index. The read side kept comparing exactly, while every E2E caller passes SCREAMING_CASE —
// API_ROUTE, E2E_SPEC, PAGE_ROUTE, PAGE_OBJECT, USER_FLOW — so `'api_route' = 'API_ROUTE'` was false
// and ListGapsE2E found no routes and produced no gaps. JS/TS unit gap listing broke the same way
// (FUNCTION / METHOD / VARIABLE). Java unit generation kept working only because its literal is
// already lowercase, which made the failure look E2E-specific.
//
// This asserts against a real server because the defect is entirely in SQL comparison semantics.
func TestSymbolKindLookupIsCaseInsensitive(t *testing.T) {
	url, why := ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	st, err := Open(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	const file = "src/main/java/com/acme/OrderRoutes.java"
	t.Cleanup(func() {
		_, _ = st.DeleteSymbolsByFile(ctx, "kindcase/repo", file)
		_, _ = st.DeleteFile(ctx, "kindcase/repo", file)
	})
	_, _ = st.DeleteSymbolsByFile(ctx, "kindcase/repo", file)

	// The files row must carry the same repo_id as the symbol: ListSymbolsInNonTestFiles joins on
	// (file, repo_id) now, so an unscoped row would not join and the test would pass or fail for
	// the wrong reason.
	if err := st.UpsertFile(ctx, &File{File: file, SHA: "s1", Lang: "java", IsTest: false, RepoID: "kindcase/repo"}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	// Callers hand the indexer SCREAMING_CASE kinds; InsertSymbol lowercases them on the way in.
	if _, err := st.InsertSymbol(ctx, &Symbol{
		Lang: "java", Kind: "API_ROUTE", FQName: "com.acme.OrderRoutes#place",
		File: file, StartLine: 10, EndLine: 20, RepoID: "kindcase/repo",
	}); err != nil {
		t.Fatalf("InsertSymbol: %v", err)
	}

	// This is verbatim what ListGapsE2E asks for.
	got, err := st.ListSymbolsInNonTestFiles(ctx, "kindcase/repo", "java", "API_ROUTE")
	if err != nil {
		t.Fatalf("ListSymbolsInNonTestFiles: %v", err)
	}
	var found bool
	for _, sym := range got {
		if sym != nil && sym.FQName == "com.acme.OrderRoutes#place" {
			found = true
		}
	}
	if !found {
		t.Error("an API_ROUTE symbol is invisible to the query ListGapsE2E runs; " +
			"no E2E gaps can be produced and no E2E tests will be generated")
	}

	// Lowercase must keep working, so the fix is a widening rather than a swap.
	got, err = st.ListSymbolsInNonTestFiles(ctx, "kindcase/repo", "java", "api_route")
	if err != nil {
		t.Fatalf("ListSymbolsInNonTestFiles(lower): %v", err)
	}
	if len(got) == 0 {
		t.Error("lowercase kind lookup regressed")
	}
}
