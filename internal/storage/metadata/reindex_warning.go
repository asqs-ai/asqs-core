package metadata

import (
	"context"
	"fmt"
	"strings"
)

// UnscopedRowCounts reports how many symbols, edges and files still carry an empty repo_id.
//
// Such rows are invisible to every scoped read: they are not deleted, not returned, and not
// counted. A deployment that upgrades without running the migration — or one whose migration could
// not resolve a path claimed by several repositories — keeps its data but stops seeing it. That is
// recoverable by reindexing, and unrecoverable only if nobody notices, which is what this exists
// to prevent.
type UnscopedRowCounts struct {
	Symbols int64
	Edges   int64
	Files   int64
}

// Total is the number of rows no scoped query can reach.
func (c UnscopedRowCounts) Total() int64 { return c.Symbols + c.Edges + c.Files }

// ReposMissingFileRows lists repositories that have symbols but no `files` rows at all.
//
// This detects a failure the unscoped-row count cannot see. When file upserts fail — as they do on
// a database whose `files` primary key predates (repo_id, file), where ON CONFLICT (repo_id, file)
// raises SQLSTATE 42P10 — the result is not badly-scoped rows but NO rows, and a count of unscoped
// rows returns zero, which reads as healthy.
//
// The consequence is severe and silent: `files` is what change detection, gap listing, the
// documentation plan and the overview all read, so the repository indexes "successfully" and then
// plans nothing. An indexed repository with symbols and no files is never legitimate.
func (s *Store) ReposMissingFileRows(ctx context.Context) ([]string, error) {
	const q = `
SELECT DISTINCT s.repo_id
  FROM symbols s
 WHERE s.repo_id <> ''
   AND NOT EXISTS (SELECT 1 FROM files f WHERE f.repo_id = s.repo_id)
 ORDER BY s.repo_id`
	rows, err := s.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("repos missing file rows: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan repo id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MissingFileRowsWarning returns the operator-facing warning for repositories with symbols but no
// file rows, or "" when there are none.
func MissingFileRowsWarning(repos []string) string {
	if len(repos) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"BROKEN INDEX: %d repositor(y/ies) have symbols but no `files` rows: %s. Every read of `files` "+
			"— incremental change detection, gap listing, the documentation plan and the overview — sees "+
			"an empty repository, so runs complete successfully and plan nothing. The usual cause is file "+
			"upserts failing against a `files` table still keyed on `file` alone; starting this binary "+
			"applies the key change, after which a fresh index run repairs the rows.",
		len(repos), strings.Join(repos, ", "))
}

// CountUnscopedRows counts rows with an empty repo_id in the three repo-scoped tables.
func (s *Store) CountUnscopedRows(ctx context.Context) (UnscopedRowCounts, error) {
	var out UnscopedRowCounts
	const q = `
SELECT (SELECT count(*) FROM symbols WHERE repo_id = ''),
       (SELECT count(*) FROM edges   WHERE repo_id = ''),
       (SELECT count(*) FROM files   WHERE repo_id = '')`
	if err := s.db.QueryRow(ctx, q).Scan(&out.Symbols, &out.Edges, &out.Files); err != nil {
		return UnscopedRowCounts{}, fmt.Errorf("count unscoped rows: %w", err)
	}
	return out, nil
}

// ReindexRequiredWarning returns the operator-facing warning for unscoped rows, or "" when there
// are none.
//
// The wording names the remedy rather than the symptom. An operator reading "3 unscoped rows" has
// to work out what to do; one reading "reindex each repository" does not.
func ReindexRequiredWarning(c UnscopedRowCounts) string {
	if c.Total() == 0 {
		return ""
	}
	return fmt.Sprintf(
		"--reindex-required: %d metadata row(s) predate repository scoping (symbols=%d, edges=%d, files=%d) "+
			"and are INVISIBLE to every query until they are rewritten. Run a full index pass for each "+
			"repository in this database. Multi-repository installs whose paths already collided cannot be "+
			"backfilled automatically — the reindex is the fix, not an optimisation.",
		c.Total(), c.Symbols, c.Edges, c.Files)
}

// ListRepoIDs returns the distinct non-empty repo_ids present in symbols, sorted.
//
// Repository scoping made "which repositories are in this database" a question with an answer, and
// several callers need it: operators triaging a shared database, and read-only tests that must pick
// a repository out of whatever corpus they are pointed at rather than assuming one.
func (s *Store) ListRepoIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT repo_id FROM symbols WHERE repo_id <> '' ORDER BY repo_id`)
	if err != nil {
		return nil, fmt.Errorf("list repo ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan repo id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
