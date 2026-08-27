package indexer

import (
	"os"
	"strings"
	"testing"
)

// symbolBodyHash answers "did the SOURCE of this symbol change", so it hashes the line span rather
// than the chunk: chunking has its own budgets and overlap, and a chunking change would otherwise
// register as churn on code nobody touched.
func TestSymbolBodyHash_tracksTheSourceSpan(t *testing.T) {
	src := "package a\nfunc x() {\n  return 1\n}\nfunc y() {}\n"
	h1 := symbolBodyHash(src, 2, 4)
	if h1 == "" || len(h1) != 64 {
		t.Fatalf("hash = %q, want a sha256 hex string", h1)
	}
	// The same span in an unchanged file hashes the same — this is what makes churn count CHANGES
	// rather than passes.
	if h2 := symbolBodyHash(src, 2, 4); h2 != h1 {
		t.Error("the same span hashed differently twice")
	}
	// A change inside the span moves it.
	changed := "package a\nfunc x() {\n  return 2\n}\nfunc y() {}\n"
	if symbolBodyHash(changed, 2, 4) == h1 {
		t.Error("an edit inside the span did not change the hash")
	}
	// A change OUTSIDE the span does not.
	elsewhere := "package a\nfunc x() {\n  return 1\n}\nfunc y() { z() }\n"
	if symbolBodyHash(elsewhere, 2, 4) != h1 {
		t.Error("an edit outside the span changed the hash; churn would fire on untouched symbols")
	}
}

// Out-of-range spans must not panic. Indexers disagree about end lines, and a symbol whose declared
// span runs past the file is a parser quirk, not a reason to fail a run.
func TestSymbolBodyHash_toleratesBadSpans(t *testing.T) {
	src := "a\nb\nc\n"
	for _, tc := range [][2]int{{0, 2}, {1, 99}, {5, 6}, {3, 1}, {-4, -1}} {
		if got := symbolBodyHash(src, tc[0], tc[1]); len(got) != 64 {
			t.Errorf("span %v produced %q, want a hash", tc, got)
		}
	}
	if symbolBodyHash("", 1, 1) == "" {
		t.Error("empty source produced no hash")
	}
}

// GUARD: the reindex path must UPSERT then PRUNE, never delete first.
//
// Deleting a file's symbols before re-inserting them is what the old flow did, and it destroys the
// ids CP13 exists to preserve — silently, since the run still succeeds and the symbols are all
// there. What breaks is everything hanging off the id: chunks.symbol_id dangles, and each symbol's
// history restarts every run, so churn is permanently 1.
//
// It also has to clear the file's outbound edges explicitly, because the delete-symbols cascade used
// to do that as a side effect. Without it, a call removed from the source lingers as an edge forever.
func TestReindexUpsertsRatherThanDeletingSymbols(t *testing.T) {
	b, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// The removed-file path still deletes; the CHANGED-file path must not.
	changed := src[strings.Index(src, "// Chunks are still delete-then-write"):]
	if i := strings.Index(changed, "DeleteSymbolsByFile(ctx"); i >= 0 && i < strings.Index(changed, "DeleteSymbolsByFileExcept") {
		t.Error("the reindex path deletes a file's symbols before inserting; stable ids are destroyed " +
			"and chunks.symbol_id dangles")
	}
	for _, want := range []string{
		"DeleteSymbolsByFileExcept(ctx, opts.RepoID, parsed.Path, ids)",
		"DeleteOutboundEdgesForFile(ctx, opts.RepoID, parsed.Path)",
		"InsertSymbolVersions(ctx, versions)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the reindex path does not call %s", want)
		}
	}
	// History must never fail a run.
	if !strings.Contains(src, "History is auxiliary") {
		t.Error("a symbol_versions failure is not documented as non-fatal; losing a churn observation " +
			"must not fail an index run")
	}
}
