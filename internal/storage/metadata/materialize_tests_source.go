package metadata

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// materializeTestsSourceMaxAttempts bounds how many times MaterializeTestsSourceEdges will
// retry the materialization transaction when the underlying connection is reported as bad.
// driver.ErrBadConn is auto-retried by database/sql for new connections, but once a Tx is
// pinned to a connection any mid-transaction failure (typical when a backend restarts or a
// pool entry has been idle past wait_timeout) surfaces as "driver: bad connection" without
// retry. Retrying the whole materialization with a fresh connection is safe because the
// transaction is rolled back and recreated from scratch.
const materializeTestsSourceMaxAttempts = 3

// MaterializeTestsSourceEdges rebuilds all TESTS_SOURCE rows: deletes existing edges of that type, then
// (1) inserts from **calls** and **imports** where the caller lives in an **is_test** file and the callee in a non-test file,
// (2) adds **naming-convention** links: test class `FooTest` / `FooIT` / `FooTests` in package P → production class `P.Foo`.
//
// This is a **heuristic** traceability layer (static analysis), not execution coverage — see docs/DOCUMENTATION.md.
//
// The materialization is wrapped in a small retry loop for transient bad-connection errors
// (see materializeTestsSourceMaxAttempts). Indexer runs reported "TESTS_SOURCE materialization
// failed: driver: bad connection" when a pooled connection had been silently closed by the
// backend; retrying with a fresh transaction recovers without affecting correctness.
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE edge_type = $1`, EdgeTypeTestsSource); err != nil {
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
	if _, err := tx.ExecContext(ctx, q, EdgeTypeTestsSource); err != nil {
		return 0, fmt.Errorf("metadata: insert TESTS_SOURCE from calls/imports: %w", err)
	}

	if err := insertTestsSourceFromNamingConvention(ctx, tx); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)::int FROM edges WHERE edge_type = $1`, EdgeTypeTestsSource).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// isTransientConnError reports whether err is a recoverable connection error worth retrying
// the whole materialization for. driver.ErrBadConn is the canonical sentinel; pgx/lib/pq
// can also surface the message as a wrapped string. Caller callers must NOT retry on other
// errors (constraint violations, syntax errors, etc.).
func isTransientConnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
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
	return false
}

func insertTestsSourceFromNamingConvention(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
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
		err := tx.QueryRowContext(ctx, `
			SELECT s.id FROM symbols s
			INNER JOIN files f ON f.file = s.file
			WHERE s.fq_name = $1 AND LOWER(s.kind) = 'class' AND f.is_test = FALSE
			LIMIT 1`, sutFQ).Scan(&sutID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return err
		}
		_, err = tx.ExecContext(ctx, `
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
