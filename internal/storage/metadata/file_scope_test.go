package metadata

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The `files` row delete must not cross repository boundaries.
//
// History, because the assertion changed shape and the reason matters. `files` was keyed
// `file TEXT PRIMARY KEY`, so there was no repo column to filter on and the naive statement was
// `DELETE FROM files WHERE file = $1`. That is what shipped: the cross-repo fix scoped the symbol
// and chunk deletes and left this one unscoped, so removing a shared path from one repository
// stripped another repository's file metadata. The interim fix inferred ownership from
// `symbols.repo_id` with a NOT EXISTS subquery.
//
// B23 moved the key to (repo_id, file). Ownership is now a column, so the subquery is gone — not
// weakened. Each repository has its own row and cannot name another's in a delete at all.
func TestDeleteFileIsRepoScoped(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	var code []string
	for _, ln := range strings.Split(string(b), "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		code = append(code, ln)
	}
	src := strings.Join(code, "\n")

	if strings.Contains(src, "DELETE FROM files WHERE file = $1`") {
		t.Error("DeleteFile is unscoped again; removing a shared path from one repository will " +
			"delete another repository's files row, stripping its is_test/lang/module")
	}
	if !strings.Contains(src, "DELETE FROM files WHERE repo_id = $2 AND file = $1") {
		t.Error("DeleteFile must filter on files.repo_id; the (repo_id, file) key is what makes " +
			"one repository's row unreachable from another repository's delete")
	}
}

// scratchStoreForFileScopeTest opens a store against the scratch database, or skips.
func scratchStoreForFileScopeTest(t *testing.T) (*Store, context.Context) {
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

// seedSharedPath registers one path in each of the given repositories — its own `files` row and its
// own symbol per repo. Mirrors the real shape: two repositories both containing `package.json`.
//
// Under the old single-row key this seeded ONE files row shared by both repos, which is precisely
// the arrangement that made a delete ambiguous.
func seedSharedPath(t *testing.T, s *Store, ctx context.Context, path string, repoIDs ...string) {
	t.Helper()
	for i, repo := range repoIDs {
		if err := s.UpsertFile(ctx, &File{
			File: path, SHA: "sha", Lang: "typescript", Module: "web", IsTest: false, RepoID: repo,
		}); err != nil {
			t.Fatal(err)
		}
		sym := &Symbol{
			Lang: "typescript", Kind: "class",
			FQName:    fmt.Sprintf("pkg.Shared%d", i),
			File:      path,
			StartLine: 1, EndLine: 2,
			RepoID: repo,
		}
		if _, err := s.InsertSymbol(ctx, sym); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.db.Exec(bg, `DELETE FROM symbols WHERE file = $1`, path)
		_, _ = s.db.Exec(bg, `DELETE FROM files WHERE file = $1`, path)
	})
}

// fileRowExistsFor reports whether a specific repository still has a row for the path.
func fileRowExistsFor(t *testing.T, s *Store, ctx context.Context, repoID, path string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM files WHERE repo_id = $1 AND file = $2`, repoID, path).Scan(&n); err != nil {
		t.Fatalf("count files row: %v", err)
	}
	return n > 0
}

// TestDeleteFile_keepsOtherRepositorysRow is the regression test for the cross-repo delete.
//
// Failure scenario, which is the ordinary one on a shared database: repos A and B are both indexed
// and both contain `package.json`. B's working tree drops it, so B's run deletes B's chunks and
// symbols (correctly scoped) and then deleted A's `files` row. A's symbols survive but lose their
// `files` join — MaterializeTestsSourceEdges INNER JOINs `files` and drops them, GetFile returns nil
// in the retrieve path, and A's next DetectChanges reclassifies the path as new.
//
// The assertion is stronger than the interim one it replaces: B's own row must actually go (the
// subquery version had to retain it, because there was only one row to retain), while A's is
// untouched.
func TestDeleteFile_keepsOtherRepositorysRow(t *testing.T) {
	s, ctx := scratchStoreForFileScopeTest(t)
	path := fmt.Sprintf("shared-%d/package.json", time.Now().UnixNano())
	const repoA, repoB = "github.com/acme/a", "github.com/acme/b"
	seedSharedPath(t, s, ctx, path, repoA, repoB)

	// B removes the path: its symbols go first, exactly as the indexer orders it.
	if _, err := s.DeleteSymbolsByFile(ctx, repoB, path); err != nil {
		t.Fatalf("DeleteSymbolsByFile: %v", err)
	}
	deleted, err := s.DeleteFile(ctx, repoB, path)
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted {
		t.Error("B's own files row survived B's removal pass; stale rows accumulate on every removal")
	}
	if fileRowExistsFor(t, s, ctx, repoB, path) {
		t.Errorf("repo B's files row for %q outlived its own delete", path)
	}
	if !fileRowExistsFor(t, s, ctx, repoA, path) {
		t.Fatalf("repo A's files row for %q was deleted by repo B's removal pass", path)
	}

	// A's row must still carry the metadata A depends on.
	f, err := s.GetFile(ctx, repoA, path)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if f == nil {
		t.Fatal("GetFile returned nil for a path that still has a files row")
	}
	if f.Lang != "typescript" || f.Module != "web" {
		t.Errorf("surviving files row lost its metadata: lang=%q module=%q", f.Lang, f.Module)
	}

	// And B must not be able to read A's row back through the scoped getter.
	if got, gerr := s.GetFile(ctx, repoB, path); gerr != nil {
		t.Fatalf("GetFile(repoB): %v", gerr)
	} else if got != nil {
		t.Error("GetFile answered repo B with repo A's row; the read is not scoped")
	}
}

// The scoping must not leak: when this repo is the only owner, the row goes.
func TestDeleteFile_removesRowWhenSoleOwner(t *testing.T) {
	s, ctx := scratchStoreForFileScopeTest(t)
	path := fmt.Sprintf("solo-%d/package.json", time.Now().UnixNano())
	const repoA = "github.com/acme/a"
	seedSharedPath(t, s, ctx, path, repoA)

	if _, err := s.DeleteSymbolsByFile(ctx, repoA, path); err != nil {
		t.Fatalf("DeleteSymbolsByFile: %v", err)
	}
	deleted, err := s.DeleteFile(ctx, repoA, path)
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted {
		t.Error("DeleteFile kept the row although no other repository owns the path; stale files " +
			"rows would accumulate on every removal")
	}
	if fileRowExistsFor(t, s, ctx, repoA, path) {
		t.Errorf("files row for %q survived its sole owner's removal", path)
	}
}

// A row left over from before repo scoping carries an empty repo_id. It is now a SEPARATE row
// rather than the same row seen by everyone, so a scoped delete simply does not name it.
//
// The interim implementation had such a row BLOCK a scoped delete, because ownership was inferred
// from symbols and an unattributable symbol made the inference unsafe. That trade-off is gone: the
// legacy row is neither deleted nor in the way. It stays until a reindex rewrites it, which is what
// ReindexRequiredWarning tells the operator to do.
func TestDeleteFile_legacyUnscopedRowIsUntouched(t *testing.T) {
	s, ctx := scratchStoreForFileScopeTest(t)
	path := fmt.Sprintf("legacy-%d/package.json", time.Now().UnixNano())
	const repoA = "github.com/acme/a"
	seedSharedPath(t, s, ctx, path, repoA, "") // "" = a pre-migration row

	if _, err := s.DeleteSymbolsByFile(ctx, repoA, path); err != nil {
		t.Fatalf("DeleteSymbolsByFile: %v", err)
	}
	deleted, err := s.DeleteFile(ctx, repoA, path)
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if !deleted {
		t.Error("A's own row must be deletable; an unattributable legacy row is a separate row now")
	}
	if !fileRowExistsFor(t, s, ctx, "", path) {
		t.Error("the legacy unscoped row was deleted by a scoped run; it belongs to nobody and " +
			"must survive until a reindex rewrites it")
	}
}

// An unscoped run (empty repoID) must match the empty repo_id exactly, never as a wildcard.
func TestDeleteFile_emptyRepoIDIsNotAWildcard(t *testing.T) {
	s, ctx := scratchStoreForFileScopeTest(t)
	path := fmt.Sprintf("wildcard-%d/package.json", time.Now().UnixNano())
	const repoA = "github.com/acme/a"
	seedSharedPath(t, s, ctx, path, repoA)

	deleted, err := s.DeleteFile(ctx, "", path)
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if deleted {
		t.Error("an empty repoID deleted a row owned by a scoped repository; empty must match the " +
			"empty repo_id exactly, not everything")
	}
	if !fileRowExistsFor(t, s, ctx, repoA, path) {
		t.Error("scoped repository's files row destroyed by an unscoped run")
	}
}
