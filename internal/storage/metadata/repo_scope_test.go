package metadata

import (
	"os"
	"strings"
	"testing"
)

// Symbol deletes must be repo-scoped, for the same reason chunk deletes must be: the indexer clears
// a file's rows before re-inserting them, and `DELETE FROM symbols WHERE file = $1` matched every
// repository sharing that path. Two React or Angular repositories in one database would strip each
// other's symbol graph mid-run.
func TestDeleteSymbolsByFileIsRepoScoped(t *testing.T) {
	// batch.go rejoins this list when the batched insert path arrives (CP10).
	src := readMetadataSourceWithoutComments(t, "store.go")

	if strings.Contains(src, `"DELETE FROM symbols WHERE file = $1"`) {
		t.Error("DeleteSymbolsByFile is unscoped again; indexing one repository will delete another's symbols")
	}
	if !strings.Contains(src, `DELETE FROM symbols WHERE file = $1 AND repo_id = $2`) {
		t.Error("DeleteSymbolsByFile must filter on both file and repo_id")
	}
	// The scoping is inert unless writes populate repo_id.
	if !strings.Contains(src, "repo_id)") || !strings.Contains(src, "INSERT INTO symbols") {
		t.Error("InsertSymbol must persist repo_id, or the scoped delete matches nothing")
	}
}

// Reindex must not enumerate the whole database. ListFiles takes no repo filter, and reindex used it
// to decide what to delete — so reindexing one repository erased every other indexed repository
// before doing any work of its own. ListSymbolFilesByRepo is what makes that question answerable.
func TestListSymbolFilesByRepoExists(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	if !strings.Contains(string(b), "func (s *Store) ListSymbolFilesByRepo") {
		t.Fatal("ListSymbolFilesByRepo is gone; reindex has no repo-scoped way to list what to delete")
	}
	if !strings.Contains(string(b), "SELECT DISTINCT file FROM symbols WHERE repo_id = $1") {
		t.Error("ListSymbolFilesByRepo must filter on repo_id")
	}
}
