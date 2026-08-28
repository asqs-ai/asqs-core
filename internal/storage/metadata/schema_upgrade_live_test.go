package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// InitSchema must repair a database whose `files` table predates the (repo_id, file) primary key.
//
// This is the regression test for a live failure. `UpsertFile` targets ON CONFLICT (repo_id, file);
// the composite key was declared only inside `CREATE TABLE IF NOT EXISTS`, which is a no-op on an
// existing table, and moved only by migration 0006, which is run by hand. On a database still keyed
// on `file` alone, every file upsert failed with SQLSTATE 42P10 — and the indexer discarded the
// error. The observed result was a run that stored 367 symbols, wrote ZERO `files` rows, reported
// success, and then produced an empty test plan, an empty documentation plan and
// "overview: no non-test source files", because every one of those reads `files`.
//
// Every prior test opened a FRESH database, where CREATE TABLE produced the new key — which is
// exactly why none of them caught it. This one builds the old shape first.
func TestInitSchema_movesFilesPrimaryKeyOnAnUpgradedDatabase(t *testing.T) {
	url, why := ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Build the pre-B23 shape in its own schema so this cannot disturb the real tables.
	sch := fmt.Sprintf("legacy_files_%d", time.Now().UnixNano())
	for _, q := range []string{
		fmt.Sprintf(`CREATE SCHEMA %s`, sch),
		fmt.Sprintf(`SET search_path TO %s, public`, sch),
		`CREATE TABLE files (
			file    TEXT PRIMARY KEY,
			sha     TEXT NOT NULL,
			lang    TEXT NOT NULL,
			module  TEXT NOT NULL DEFAULT '',
			is_test BOOLEAN NOT NULL DEFAULT FALSE)`,
		// A row written before scoping, to prove the key move does not lose data.
		`INSERT INTO files (file, sha, lang) VALUES ('src/Legacy.java', 'sha0', 'java')`,
	} {
		if _, err := s.db.Exec(ctx, q); err != nil {
			t.Fatalf("legacy fixture %q: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, sch))
	})

	if err := s.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema on an upgraded database: %v", err)
	}

	var cols string
	if err := s.db.QueryRow(ctx, fmt.Sprintf(`
SELECT COALESCE(string_agg(a.attname, ',' ORDER BY k.ord), '')
  FROM pg_constraint c
  JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
 WHERE c.conrelid = '%s.files'::regclass AND c.contype = 'p'`, sch)).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != "repo_id,file" {
		t.Fatalf("files primary key is %q after InitSchema, want repo_id,file — UpsertFile's "+
			"ON CONFLICT (repo_id, file) fails with SQLSTATE 42P10 against this shape, silently "+
			"producing an index run with no file rows and therefore an empty plan", cols)
	}

	// The pre-existing row must survive the key move.
	var n int
	if err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*)::int FROM %s.files WHERE file = 'src/Legacy.java'`, sch)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the legacy row was lost while moving the primary key (found %d)", n)
	}

	// And the upsert the indexer actually issues must now work.
	if _, err := s.db.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.files (file, sha, lang, module, is_test, repo_id)
		VALUES ('src/New.java','sha1','java','m',false,'github.com/acme/x')
		ON CONFLICT (repo_id, file) DO UPDATE SET sha = 'sha1'`, sch)); err != nil {
		t.Errorf("the indexer's file upsert still fails after InitSchema: %v", err)
	}

	// Re-running must be a no-op rather than an error.
	if err := s.InitSchema(ctx); err != nil {
		t.Errorf("InitSchema is not idempotent once the key has moved: %v", err)
	}
}
