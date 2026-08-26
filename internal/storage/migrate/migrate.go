// Package migrate runs one-shot schema and data migrations that cannot live in schema.sql.
//
// Why this exists: both stores apply their schema by reading an embedded schema.sql and executing
// every statement on **every process start** (metadata.Store.InitSchema, embeddings.Store.InitSchema).
// That works for idempotent DDL — CREATE TABLE IF NOT EXISTS, ALTER TABLE ... ADD COLUMN IF NOT
// EXISTS — and is the established pattern there. It cannot host:
//
//   - **Data backfills.** An UPDATE in schema.sql would re-run on every boot, rewriting the whole
//     table each time a process starts.
//   - **CREATE INDEX CONCURRENTLY.** It cannot run inside a transaction block, and it is the only
//     safe way to add an index to a live table.
//   - **Anything expensive.** A table rewrite (ADD COLUMN ... GENERATED ... STORED) must be a
//     deliberate operator action, not a side effect of a restart.
//
// Migrations are recorded in schema_migrations by id, so running the command twice is a no-op.
// Each migration must still be written to be safe if it is interrupted and re-run.
package migrate

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is a single one-shot step. Apply must be safe to re-run if it fails partway.
type Migration struct {
	// ID is the stable identifier recorded in schema_migrations. Never reuse or renumber.
	ID string
	// Description is shown to the operator while it runs.
	Description string
	// Concurrent marks migrations that must NOT run inside a transaction (CREATE INDEX CONCURRENTLY).
	Concurrent bool
	// Apply performs the migration against the pool.
	Apply func(ctx context.Context, pool *pgxpool.Pool) error
}

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    id          TEXT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms BIGINT NOT NULL DEFAULT 0
)`

// Result reports what a Run did.
type Result struct {
	Applied []string
	Skipped []string
}

// Run applies every migration in ms that has not been recorded yet, in slice order.
// Progress is written to out when non-nil.
func Run(ctx context.Context, pool *pgxpool.Pool, ms []Migration, out io.Writer) (Result, error) {
	var res Result
	if pool == nil {
		return res, fmt.Errorf("migrate: nil pool")
	}
	if _, err := pool.Exec(ctx, schemaMigrationsDDL); err != nil {
		return res, fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	applied, err := appliedIDs(ctx, pool)
	if err != nil {
		return res, err
	}
	for _, m := range ms {
		if _, done := applied[m.ID]; done {
			res.Skipped = append(res.Skipped, m.ID)
			logf(out, "  skip  %s (already applied)\n", m.ID)
			continue
		}
		logf(out, "  apply %s — %s\n", m.ID, m.Description)
		start := time.Now()
		if err := m.Apply(ctx, pool); err != nil {
			return res, fmt.Errorf("migrate %s: %w", m.ID, err)
		}
		dur := time.Since(start)
		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations (id, duration_ms) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			m.ID, dur.Milliseconds()); err != nil {
			return res, fmt.Errorf("migrate: record %s: %w", m.ID, err)
		}
		res.Applied = append(res.Applied, m.ID)
		logf(out, "        done in %s\n", dur.Round(time.Millisecond))
	}
	return res, nil
}

// Pending returns the ids in ms that have not been applied yet.
func Pending(ctx context.Context, pool *pgxpool.Pool, ms []Migration) ([]string, error) {
	if _, err := pool.Exec(ctx, schemaMigrationsDDL); err != nil {
		return nil, err
	}
	applied, err := appliedIDs(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range ms {
		if _, done := applied[m.ID]; !done {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func appliedIDs(ctx context.Context, pool *pgxpool.Pool) (map[string]struct{}, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func logf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
