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
//
// ID 0002 is reserved: upstream's body (chunk_metadata/module/path indexes) arrives with the
// bundle that tunes those filters, and reusing or renumbering an ID would desynchronize the
// ledger from upstream's.
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
		{
			ID:          "0003_chunks_content_tsv_gin",
			Description: "GIN index on chunks.content_tsv for the lexical retrieval channel",
			Concurrent:  true,
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// The column is declared in schema.sql too, but `asqs-core migrate` connects with a
				// raw pool and never runs schema.sql — so on a corpus indexed before the lexical
				// channel existed, and never restarted through the pipeline since, the column is
				// absent and the index build fails with "column content_tsv does not exist". A
				// migration has to be self-contained; this ADD COLUMN is the same idempotent DDL
				// schema.sql carries, so running both is harmless.
				//
				// Adding a STORED generated column rewrites the table — this migration is where an
				// operator schedules that cost. The GIN index must be CONCURRENTLY for the same
				// reason as the others.
				if _, err := pool.Exec(ctx, `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS content_tsv tsvector
					   GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED`); err != nil {
					return fmt.Errorf("add content_tsv column: %w", err)
				}
				if _, err := pool.Exec(ctx, `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chunks_content_tsv
					   ON chunks USING GIN (content_tsv)`); err != nil {
					return fmt.Errorf("create content_tsv index: %w", err)
				}
				return nil
			},
		},
	}
}

// MetadataMigrations are one-shot migrations for the metadata database.
//
// IDs 0002–0003 are reserved: upstream's bodies for those (the simple_name and trigram lookup
// aids) arrive with the bundle that adds their readers, and reusing or renumbering an ID would
// desynchronize the ledger from upstream's.
func MetadataMigrations() []Migration {
	return []Migration{
		{
			ID:          "0001_lowercase_symbol_lang_kind",
			Description: "lowercase symbols.lang / symbols.kind so queries can drop LOWER() and use an index",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// Queries used LOWER(s.lang) = LOWER($1), which defeats idx_symbols_lang because
				// there is no matching expression index. Normalizing at write time (InsertSymbol)
				// plus this one-shot backfill lets the queries compare directly.
				for _, q := range []string{
					`UPDATE symbols SET lang = lower(lang) WHERE lang <> lower(lang)`,
					`UPDATE symbols SET kind = lower(kind) WHERE kind <> lower(kind)`,
					`UPDATE edges SET edge_type = upper(edge_type) WHERE edge_type <> upper(edge_type)`,
				} {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("normalize case: %w", err)
					}
				}
				return nil
			},
		},
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
			ID:          "0005_symbols_degree_columns",
			Description: "materialize in/out degree on symbols so gap listing stops issuing one edge query per candidate",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// Gap listing calls GetEdgesTo once per candidate symbol to compute a centrality
				// signal. On a 30k-symbol repository that is 30k queries per run, all of them
				// answerable by a column read.
				//
				// Three columns, not two. The centrality check counts inbound edges EXCLUDING
				// TESTS_SOURCE: a test that covers a symbol creates an inbound edge, and counting it
				// would inflate the "central dependency, under-tested" signal for precisely the
				// symbols that already have tests. A plain in_degree is therefore not a drop-in
				// replacement for what the code computes, and swapping one in would quietly reorder
				// gap priorities — the ordering this wave makes deterministic and measurable.
				stmts := []string{
					`ALTER TABLE symbols ADD COLUMN IF NOT EXISTS in_degree INTEGER NOT NULL DEFAULT 0`,
					`ALTER TABLE symbols ADD COLUMN IF NOT EXISTS out_degree INTEGER NOT NULL DEFAULT 0`,
					`ALTER TABLE symbols ADD COLUMN IF NOT EXISTS in_degree_non_test INTEGER NOT NULL DEFAULT 0`,
					`CREATE INDEX IF NOT EXISTS idx_symbols_in_degree_non_test ON symbols (in_degree_non_test)`,
				}
				for _, q := range stmts {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("degree columns: %w", err)
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
		{
			ID:          "0002_symbols_trigram_and_simple_name",
			Description: "pg_trgm + fq_name trigram index, generated simple_name column and index",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// ADD COLUMN ... GENERATED ... STORED rewrites the table on PG 12+, which is
				// precisely why it is an operator action rather than a startup side effect.
				stmts := []string{
					`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
					// ListSymbolsByFQSubstring used strpos(lower(fq_name), lower($1)) — unindexable.
					`ALTER TABLE symbols ADD COLUMN IF NOT EXISTS simple_name TEXT
					   GENERATED ALWAYS AS (regexp_replace(fq_name, '^.*[.#]', '')) STORED`,
				}
				for _, s := range stmts {
					if _, err := pool.Exec(ctx, s); err != nil {
						return fmt.Errorf("symbols schema: %w", err)
					}
				}
				return nil
			},
		},
		{
			ID:          "0003_symbols_lookup_indexes",
			Description: "trigram, simple_name and lower(lang) indexes for symbol lookup",
			Concurrent:  true,
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				stmts := []string{
					`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_fq_trgm
					   ON symbols USING GIN (fq_name gin_trgm_ops)`,
					// NOTE: this becomes (repo_id, simple_name) when the symbol graph is
					// repo-scoped (Spec 1 / B23); the composite index supersedes this one.
					`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_simple_name
					   ON symbols (simple_name)`,
					`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_lang_lower
					   ON symbols (lower(lang))`,
				}
				for _, s := range stmts {
					if _, err := pool.Exec(ctx, s); err != nil {
						return fmt.Errorf("create index: %w", err)
					}
				}
				return nil
			},
		},
		{
			ID:          "0007_symbols_repo_scoped_lookup_indexes",
			Description: "composite (repo_id, ...) lookup indexes so scoped reads stay indexed",
			Concurrent:  true,
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// Every symbol lookup gained a repo_id predicate in B23. Without these the planner
				// filters on repo_id after an index scan on the old single-column indexes, which is
				// correct but reads the whole matching set of every repository first.
				stmts := []string{
					`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_repo_fq_name
					   ON symbols (repo_id, fq_name)`,
					`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_repo_lang_kind
					   ON symbols (repo_id, lang, kind)`,
				}
				for _, q := range stmts {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("repo-scoped lookup indexes: %w", err)
					}
				}

				// simple_name is OPTIONAL, and this migration must not assume otherwise.
				//
				// 0002 adds it as a STORED generated column, which rewrites the table — deliberately
				// an operator action, not a startup side effect. The read path treats it the same
				// way: hasSimpleNameColumn() probes for it and falls back to the unindexed predicate
				// when it is absent.
				//
				// This migration originally assumed 0002's column was present and failed the whole
				// run with `column "simple_name" does not exist` on a database whose ledger recorded
				// 0002 as applied but whose `symbols` table had since been recreated without it. That
				// took the two indexes above down with it: CONCURRENTLY runs outside a transaction,
				// so a later statement failing does not undo earlier ones, but the ledger is not
				// written either — leaving the operator with a migration that reports failure and
				// half its work done.
				//
				// Skipping is correct rather than lenient: the index accelerates a lookup that
				// already degrades gracefully, so its absence costs latency, not correctness. Adding
				// the column here would rewrite a large table inside a migration whose stated job is
				// index creation.
				var hasSimpleName bool
				if err := pool.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_schema = current_schema()
						  AND table_name = 'symbols'
						  AND column_name = 'simple_name'
					)`).Scan(&hasSimpleName); err != nil {
					return fmt.Errorf("probe simple_name: %w", err)
				}
				if !hasSimpleName {
					return nil
				}
				// 0003 created this on simple_name alone and noted it becomes
				// (repo_id, simple_name) when the symbol graph is repo-scoped. It now is.
				if _, err := pool.Exec(ctx, `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_symbols_repo_simple_name
					   ON symbols (repo_id, simple_name)`); err != nil {
					return fmt.Errorf("repo-scoped simple_name index: %w", err)
				}
				return nil
			},
		},
		{
			ID:          "0008_simple_name_parameter_aware",
			Description: "recreate generated simple_name to strip B25 parameter lists and generic markers",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				// B25 parameterizes C# FQNames ("Ns.Type<T>#M(int,Outer.Inner)"). 0002's generated
				// expression takes the segment after the LAST '.' or '#', which inside a parameter
				// list yields garbage like "Inner)" — and a generated column's expression cannot be
				// altered in place, so the column is dropped and recreated. Guarded on existence:
				// the column is 0002's operator opt-in, and a database without it stays without it.
				// The dependent indexes (0003's and 0007's) drop with the column and are recreated
				// here; plain CREATE INDEX, not CONCURRENTLY, because ADD COLUMN ... STORED just
				// rewrote the table anyway.
				//
				// Expression mirrored from metadata.BareFQName (the Go twin — change both or
				// neither): strip "(...)" to end-of-string, strip "<...>" runs, then take the last
				// [.#] segment.
				var hasSimpleName bool
				if err := pool.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_schema = current_schema()
						  AND table_name = 'symbols'
						  AND column_name = 'simple_name'
					)`).Scan(&hasSimpleName); err != nil {
					return fmt.Errorf("probe simple_name: %w", err)
				}
				if !hasSimpleName {
					return nil
				}
				stmts := []string{
					`ALTER TABLE symbols DROP COLUMN simple_name`,
					`ALTER TABLE symbols ADD COLUMN simple_name TEXT
					   GENERATED ALWAYS AS (
					     regexp_replace(
					       regexp_replace(
					         regexp_replace(fq_name, '\(.*$', ''),
					         '<[^#]*', '', 'g'),
					       '^.*[.#]', '')
					   ) STORED`,
					`CREATE INDEX IF NOT EXISTS idx_symbols_simple_name ON symbols (simple_name)`,
					`CREATE INDEX IF NOT EXISTS idx_symbols_repo_simple_name ON symbols (repo_id, simple_name)`,
				}
				for _, q := range stmts {
					if _, err := pool.Exec(ctx, q); err != nil {
						return fmt.Errorf("recreate simple_name: %w", err)
					}
				}
				return nil
			},
		},
	}
}
