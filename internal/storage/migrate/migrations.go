package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration bodies are ported as core reaches the bundles that need them — not up front, where
// they would reference columns and tables core does not have yet.
//
// Rules for adding one (learned upstream, each the expensive way):
//   - Never reuse or renumber an ID; schema_migrations is keyed by it.
//   - A migration may not assume any DDL from schema.sql has been applied — `asqs-core migrate`
//     connects with a raw pool and never runs InitSchema (deliberate: InitSchema also aligns the
//     embedding column and can truncate). Create what you index.
//   - pgvector defines no l2_norm for the vector type; use sqrt(inner_product(v, v)).
// The guard tests in this package enforce the last two on the source.

// EmbeddingsMigrations are one-shot migrations for the embeddings (pgvector) database.
func EmbeddingsMigrations() []Migration {
	return []Migration{
		{
			ID:          "0001_normalize_chunk_embeddings",
			Description: "L2-normalize stored chunk embeddings so the L2 index and cosine scoring agree",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// Normalizing in place is far cheaper than a full re-embed and is exactly
				// equivalent: scaling a vector to unit length preserves its direction, which is
				// all cosine depends on.
				//
				// Rows already at unit length are skipped so a re-run is cheap and so a partially
				// completed run resumes rather than restarting.
				//
				// The norm is sqrt(inner_product(v,v)) rather than l2_norm(v): pgvector defines
				// l2_norm only for halfvec and sparsevec, so l2_norm(embedding) fails with
				// "function l2_norm(vector) is not unique" — the planner sees two candidates and
				// neither takes a vector. inner_product(vector,vector) does exist, and
				// l2_normalize(vector) is pgvector's own scaling function, which also handles the
				// zero vector (it returns the zero vector rather than dividing by zero).
				const q = `
UPDATE chunks
   SET embedding = l2_normalize(embedding)
 WHERE embedding IS NOT NULL
   AND inner_product(embedding, embedding) > 0
   AND abs(sqrt(inner_product(embedding, embedding)) - 1) > 1e-6`
				tag, err := pool.Exec(ctx, q)
				if err != nil {
					return fmt.Errorf("normalize embeddings: %w", err)
				}
				_ = tag
				return nil
			},
		},
	}
}

// MetadataMigrations are one-shot migrations for the metadata database.
//
// IDs 0001–0003 and 0005 are reserved: upstream's bodies for those (case normalization,
// simple_name/trigram aids, degree columns) arrive with the bundles that add their readers, and
// reusing or renumbering an ID would desynchronize the ledger from upstream's.
func MetadataMigrations() []Migration {
	return []Migration{
		{
			ID:          "0004_symbols_repo_id",
			Description: "repo_id on symbols so per-file deletes cannot cross repositories",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// `symbols` and `files` were keyed by file path alone, while the indexer deletes
				// stale rows per file before re-inserting:
				//
				//     DELETE FROM symbols WHERE file = $1
				//
				// With two repositories in one database, indexing repo B deleted repo A's symbols
				// for every shared path — `package.json`, `README.md`, `src/app/app.module.ts` —
				// silently, mid-run. Two React or Angular repos would strip each other bare.
				//
				// This is the data-loss half of the repo-scoping work and deliberately not the
				// rest of it: scoping deletes stops rows being destroyed; scoping reads is the
				// store's design (see 0006).
				stmts := []string{
					`ALTER TABLE symbols ADD COLUMN IF NOT EXISTS repo_id TEXT NOT NULL DEFAULT ''`,
					// Backfill from chunks, the only table that already carried repo_id. A file
					// indexed by exactly one repo resolves unambiguously; one claimed by several
					// is left at '' rather than guessed, and the next index run per repo fixes it.
					`UPDATE symbols s SET repo_id = c.repo_id
					   FROM (SELECT file, min(repo_id) AS repo_id FROM chunks
					          WHERE repo_id <> '' GROUP BY file HAVING count(DISTINCT repo_id) = 1) c
					  WHERE s.file = c.file AND s.repo_id = ''`,
					`CREATE INDEX IF NOT EXISTS idx_symbols_repo_file ON symbols (repo_id, file)`,
				}
				for _, s := range stmts {
					if _, err := pool.Exec(ctx, s); err != nil {
						return fmt.Errorf("repo-scope symbols/files: %w", err)
					}
				}
				return nil
			},
		},
		{
			ID:          "0006_repo_scope_edges_and_files",
			Description: "repo_id on edges and files; files primary key becomes (repo_id, file)",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// 0004 scoped symbols so per-file DELETEs stopped destroying another repository's
				// rows, and said explicitly that scoping READS was left for later. This is that.
				//
				// `files.file` is a repo-RELATIVE path used as the whole primary key, so `pom.xml`,
				// `package.json` and `src/index.ts` are one row shared by every repository that has
				// one. Two consequences, both silent: DetectChanges compares the incoming SHA
				// against whichever repository wrote last and skips files that really did change,
				// and GetFile hands retrieval another repository's language/module/is_test.
				//
				// Ordering matters. The column is added and backfilled BEFORE the key moves,
				// because the new key cannot be created while rows still collide on it — and after
				// the backfill they cannot, since the old key already made `file` unique.
				stmts := []string{
					`ALTER TABLE edges ADD COLUMN IF NOT EXISTS repo_id TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE files ADD COLUMN IF NOT EXISTS repo_id TEXT NOT NULL DEFAULT ''`,
					// Single-repository installs first, and they are the overwhelming majority.
					// When index_runs names exactly one repository, every unscoped row in this
					// database belongs to it — no inference required, and it covers the case the
					// per-table rules below cannot: a corpus indexed before repo_id existed
					// anywhere, where symbols and chunks are BOTH blank so there is nothing to
					// resolve from. Without this such an install would come back from the upgrade
					// with every scoped read returning nothing until a full reindex.
					`UPDATE symbols SET repo_id = (SELECT min(repo_id) FROM index_runs WHERE repo_id <> '')
					  WHERE repo_id = ''
					    AND (SELECT count(DISTINCT repo_id) FROM index_runs WHERE repo_id <> '') = 1`,
					`UPDATE files SET repo_id = (SELECT min(repo_id) FROM index_runs WHERE repo_id <> '')
					  WHERE repo_id = ''
					    AND (SELECT count(DISTINCT repo_id) FROM index_runs WHERE repo_id <> '') = 1`,
					`UPDATE edges SET repo_id = (SELECT min(repo_id) FROM index_runs WHERE repo_id <> '')
					  WHERE repo_id = ''
					    AND (SELECT count(DISTINCT repo_id) FROM index_runs WHERE repo_id <> '') = 1`,
					// Edges take the CALLER's repository. An edge whose endpoints sit in different
					// repositories is precisely the cross-repo mis-binding this bundle exists to
					// stop; it is left at '' rather than assigned to one side.
					`UPDATE edges e SET repo_id = s.repo_id
					   FROM symbols s
					  WHERE e.caller_symbol_id = s.id
					    AND e.repo_id = ''
					    AND s.repo_id <> ''
					    AND EXISTS (SELECT 1 FROM symbols c
					                 WHERE c.id = e.callee_symbol_id
					                   AND (c.repo_id = s.repo_id OR c.repo_id = ''))`,
					// Files resolve from symbols, the same single-owner rule 0004 used for symbols
					// themselves: a path claimed by exactly one repository resolves; a path claimed
					// by several is left at '' rather than guessed.
					`UPDATE files f SET repo_id = s.repo_id
					   FROM (SELECT file, min(repo_id) AS repo_id FROM symbols
					          WHERE repo_id <> '' GROUP BY file HAVING count(DISTINCT repo_id) = 1) s
					  WHERE f.file = s.file AND f.repo_id = ''`,
				}
				for _, q := range stmts {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("repo-scope edges/files: %w", err)
					}
				}

				// Move the files primary key. Guarded on the current key shape so a re-run is a
				// no-op: the migration ledger already prevents a second apply, but a database
				// restored from a mid-migration backup should not fail here either.
				var pkCols string
				const pkQuery = `
SELECT COALESCE(string_agg(a.attname, ',' ORDER BY k.ord), '')
  FROM pg_constraint c
  JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
 WHERE c.conrelid = 'files'::regclass AND c.contype = 'p'`
				if err := pool.QueryRow(ctx, pkQuery).Scan(&pkCols); err != nil {
					return fmt.Errorf("read files primary key: %w", err)
				}
				if pkCols == "repo_id,file" {
					return nil
				}
				keyStmts := []string{
					`ALTER TABLE files DROP CONSTRAINT IF EXISTS files_pkey`,
					`ALTER TABLE files ADD PRIMARY KEY (repo_id, file)`,
					`CREATE INDEX IF NOT EXISTS idx_files_repo_lang ON files (repo_id, lang)`,
					`CREATE INDEX IF NOT EXISTS idx_files_repo_is_test ON files (repo_id, is_test)`,
				}
				for _, q := range keyStmts {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("move files primary key: %w", err)
					}
				}
				return nil
			},
		},
	}
}
