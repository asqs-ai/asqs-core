package metadata

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// materializeTestsSourceMaxAttempts bounds how many times MaterializeTestsSourceEdges retries the
// materialization transaction after a connection-level failure. Once a Tx is pinned to a
// connection, the pool will not retry a mid-transaction failure (a backend restart or failover)
// for us; retrying the whole materialization is safe because the transaction is rolled back and
// recreated from scratch.
const materializeTestsSourceMaxAttempts = 3

// MaterializeTestsSourceEdges rebuilds all TESTS_SOURCE rows: deletes existing edges of that type, then
// (1) inserts from **calls** and **imports** where the caller lives in an **is_test** file and the callee in a non-test file,
// (2) adds **naming-convention** links: test class `FooTest` / `FooIT` / `FooTests` in package P → production class `P.Foo`.
//
// This is a **heuristic** traceability layer (static analysis), not execution coverage — see docs/DOCUMENTATION.md.
//
// The materialization is wrapped in a small retry loop for transient connection-level errors
// (see materializeTestsSourceMaxAttempts). Indexer runs on the previous database/sql stack
// reported "TESTS_SOURCE materialization failed: driver: bad connection" when a pooled
// connection had been silently closed by the backend; retrying with a fresh transaction
// recovers without affecting correctness.
func (s *Store) MaterializeTestsSourceEdges(ctx context.Context) (int, error) {
	var (
		n       int
		lastErr error
	)
	for attempt := 1; attempt <= materializeTestsSourceMaxAttempts; attempt++ {
		n, lastErr = s.materializeTestsSourceEdgesOnce(ctx)
		if lastErr == nil {
			return n, nil
		}
		if !isTransientConnError(lastErr) {
			return 0, lastErr
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("metadata: materialize TESTS_SOURCE after %d attempt(s): %w", materializeTestsSourceMaxAttempts, lastErr)
}

func (s *Store) materializeTestsSourceEdgesOnce(ctx context.Context) (inserted int, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM edges WHERE edge_type = $1`, EdgeTypeTestsSource); err != nil {
		return 0, fmt.Errorf("metadata: delete TESTS_SOURCE: %w", err)
	}

	// Call/import graph: test → production (same direction as "test references SUT").
	q := `
		INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type)
		SELECT DISTINCT e.caller_symbol_id, e.callee_symbol_id, $1
		FROM edges e
		INNER JOIN symbols sc ON sc.id = e.caller_symbol_id
		INNER JOIN symbols sv ON sv.id = e.callee_symbol_id
		INNER JOIN files fc ON fc.file = sc.file
		INNER JOIN files fv ON fv.file = sv.file
		WHERE LOWER(e.edge_type) IN ('calls', 'imports')
		  AND fc.is_test = TRUE
		  AND fv.is_test = FALSE
		ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO NOTHING`
	if _, err := tx.Exec(ctx, q, EdgeTypeTestsSource); err != nil {
		return 0, fmt.Errorf("metadata: insert TESTS_SOURCE from calls/imports: %w", err)
	}

	if err := insertTestsSourceFromNamingConvention(ctx, tx); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	var n int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM edges WHERE edge_type = $1`, EdgeTypeTestsSource).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// isTransientConnError reports whether err is a recoverable connection error worth retrying
// the whole materialization for. Callers must NOT retry on other errors (constraint violations,
// syntax errors, etc.).
//
// On native pgx the signal is a connection-class SQLSTATE or pgconn's own SafeToRetry, not
// driver.ErrBadConn — see the branches below. The database/sql sentinels are retained but dead.
//
// Caution: a protocol violation (a statement issued while a cursor from the same Tx is open) is
// NOT transient at all, and pgx names it distinctly enough — "conn busy" — that it can be left
// unmatched here, which the database/sql stack did not allow. A persistent failure that does
// match one of these branches should still be read as a bug in the statement sequence before it
// is read as an infrastructure problem.
func isTransientConnError(err error) bool {
	if err == nil {
		return false
	}
	// driver.ErrBadConn and its text can no longer be produced now that this store speaks pgx
	// directly rather than through database/sql. They are kept because they cost nothing and
	// because failures logged by older builds are only legible if the thing they name is still
	// named here.
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	// pgconn reports errors it knows were raised before the query reached the server. Those are
	// unambiguously safe to replay.
	if pgconn.SafeToRetry(err) {
		return true
	}
	// The failure this loop actually exists for: a backend restarting or failing over underneath an
	// open transaction. pgx surfaces it as a PgError with a connection-class SQLSTATE, where the
	// database/sql stack used to surface driver.ErrBadConn — so without this branch the migration
	// would have quietly turned the retry loop into a no-op for its only real use case.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "08000", "08003", "08006", "08001", "08004", // connection exception class
			"57P01", "57P02", "57P03": // admin shutdown, crash shutdown, cannot connect now
			return true
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "driver: bad connection") {
		return true
	}
	if strings.Contains(msg, "connection reset by peer") {
		return true
	}
	if strings.Contains(msg, "broken pipe") {
		return true
	}
	// pgx's own wording for a connection that died or was closed underneath the caller. Deliberately
	// NOT matching "conn busy": that is the deterministic protocol violation described above, and
	// retrying it only adds latency to a guaranteed failure.
	if strings.Contains(msg, "conn closed") || strings.Contains(msg, "unexpected EOF") {
		return true
	}
	return false
}

func insertTestsSourceFromNamingConvention(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		SELECT s.id, s.fq_name
		FROM symbols s
		INNER JOIN files f ON f.file = s.file
		WHERE f.is_test = TRUE AND LOWER(s.kind) = 'class'`)
	if err != nil {
		return fmt.Errorf("metadata: list test classes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var testClassID, fq string
		if err := rows.Scan(&testClassID, &fq); err != nil {
			return err
		}
		sutFQ := UnderTestClassFQNameFromTestClassFQ(fq)
		if sutFQ == "" {
			continue
		}
		var sutID string
		err := tx.QueryRow(ctx, `
			SELECT s.id FROM symbols s
			INNER JOIN files f ON f.file = s.file
			WHERE s.fq_name = $1 AND LOWER(s.kind) = 'class' AND f.is_test = FALSE
			LIMIT 1`, sutFQ).Scan(&sutID)
		if err != nil {
			if err == pgx.ErrNoRows {
				continue
			}
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type)
			VALUES ($1, $2, $3)
			ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO NOTHING`,
			testClassID, sutID, EdgeTypeTestsSource)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

// UnderTestClassFQNameFromTestClassFQ maps a JUnit/Surefire-style test class FQName to the inferred production class FQName.
// Examples: com.example.FooTest → com.example.Foo; com.example.FooIT → com.example.Foo; com.example.FooTests → com.example.Foo.
// Returns empty string when the name does not match a known suffix.
func UnderTestClassFQNameFromTestClassFQ(testClassFQ string) string {
	testClassFQ = strings.TrimSpace(testClassFQ)
	if testClassFQ == "" {
		return ""
	}
	dot := strings.LastIndex(testClassFQ, ".")
	simple := testClassFQ
	if dot >= 0 {
		simple = testClassFQ[dot+1:]
	}
	prefix := ""
	if dot >= 0 {
		prefix = testClassFQ[:dot+1]
	}
	for _, suf := range []string{"Tests", "Test", "IT"} {
		if strings.HasSuffix(simple, suf) && len(simple) > len(suf) {
			base := simple[:len(simple)-len(suf)]
			if base == "" {
				return ""
			}
			return prefix + base
		}
	}
	return ""
}
