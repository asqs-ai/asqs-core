package metadata

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// materializeTestsSourceMaxAttempts bounds how many times MaterializeTestsSourceEdges retries the
// materialization transaction after a connection-level failure.
//
// Historical note, because the comment that used to sit here asserted the wrong cause. The failure
// this retry loop was built for was **not** a dead pooled connection. It was a deterministic
// protocol violation in insertTestsSourceFromNamingConvention — a statement issued while a cursor
// from the same Tx was still open — which the pgx stdlib driver reported as "driver: bad
// connection" (native pgx reports it as "conn busy"). It failed on 100% of runs against any repository containing a test class, so every
// corpus indexed before that fix has zero TESTS_SOURCE edges, and the retries only added latency to
// a guaranteed failure. See the INVARIANT on insertTestsSourceFromNamingConvention.
//
// The loop is kept for genuinely transient failures: a backend restart or failover mid-transaction
// does surface the same way, and once a Tx is pinned to a connection the pool will not retry it
// for us. Retrying the whole materialization is safe because the transaction is rolled back and
// recreated from scratch.
const materializeTestsSourceMaxAttempts = 4

// materializeTestsSourceBackoff is the delay before retry N (1-based). Fixed, not jittered: the
// failures worth retrying here are connection-level (a backend restarting or failing over), not
// lock contention, so there is no thundering herd to de-synchronise.
func materializeTestsSourceBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 100 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}

// sleepFn is the clock seam so tests can assert the backoff schedule without real elapsed time.
var sleepFn = time.Sleep

// sleepCtx sleeps unless ctx is already done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sleepFn(d)
	return ctx.Err()
}

// MaterializeTestsSourceEdges rebuilds all TESTS_SOURCE rows: deletes existing edges of that type, then
// (1) inserts from **calls** and **imports** where the caller lives in an **is_test** file and the callee in a non-test file,
// (2) adds **naming-convention** links: test class `FooTest` / `FooIT` / `FooTests` in package P → production class `P.Foo`.
//
// This is a **heuristic** traceability layer (static analysis), not execution coverage — see docs/DOCUMENTATION.md.
//
// A healthy database completes on the first attempt. The retry loop exists only for connection-level
// failures; see materializeTestsSourceMaxAttempts for why that comment used to claim otherwise.
func (s *Store) MaterializeTestsSourceEdges(ctx context.Context, repoID string) (int, error) {
	var (
		n       int
		lastErr error
	)
	for attempt := 1; attempt <= materializeTestsSourceMaxAttempts; attempt++ {
		n, lastErr = s.materializeTestsSourceEdgesOnce(ctx, repoID)
		if lastErr == nil {
			return n, nil
		}
		if !isTransientConnError(lastErr) {
			return 0, lastErr
		}
		if attempt == materializeTestsSourceMaxAttempts {
			break
		}
		// Retrying immediately against the same pool re-draws the same connection. Ping forces
		// pgxpool to notice and retire a dead one, and the backoff gives a restarting/failing-over
		// backend time to accept.
		//
		// Note this cannot rescue a deterministic error that merely *looks* transient — the
		// original "three attempts, all in microseconds, 3/3 failures on four consecutive index
		// passes" was exactly that, and adding a fourth attempt with backoff made it slower rather
		// than more reliable.
		if err := sleepCtx(ctx, materializeTestsSourceBackoff(attempt)); err != nil {
			return 0, err
		}
		_ = s.db.Ping(ctx)
	}
	return 0, fmt.Errorf("metadata: materialize TESTS_SOURCE after %d attempt(s): %w", materializeTestsSourceMaxAttempts, lastErr)
}

func (s *Store) materializeTestsSourceEdgesOnce(ctx context.Context, repoID string) (inserted int, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Scoped. This DELETE was the one statement in the store with no repository predicate at all:
	// indexing any repository wiped every repository's materialized trace links, and the next run
	// of each of those repositories was the only thing that put them back.
	if _, err := tx.Exec(ctx, `DELETE FROM edges WHERE edge_type = $1 AND repo_id = $2`, EdgeTypeTestsSource, repoID); err != nil {
		return 0, fmt.Errorf("metadata: delete TESTS_SOURCE: %w", err)
	}

	// Call/import graph: test → production (same direction as "test references SUT").
	q := `
		INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type, repo_id)
		SELECT DISTINCT e.caller_symbol_id, e.callee_symbol_id, $1, $2
		FROM edges e
		INNER JOIN symbols sc ON sc.id = e.caller_symbol_id AND sc.repo_id = $2
		INNER JOIN symbols sv ON sv.id = e.callee_symbol_id AND sv.repo_id = $2
		INNER JOIN files fc ON fc.file = sc.file AND fc.repo_id = $2
		INNER JOIN files fv ON fv.file = sv.file AND fv.repo_id = $2
		WHERE e.repo_id = $2
		  AND LOWER(e.edge_type) IN ('calls', 'imports')
		  AND fc.is_test = TRUE
		  AND fv.is_test = FALSE
		ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO NOTHING`
	if _, err := tx.Exec(ctx, q, EdgeTypeTestsSource, repoID); err != nil {
		return 0, fmt.Errorf("metadata: insert TESTS_SOURCE from calls/imports: %w", err)
	}

	if err := insertTestsSourceFromNamingConvention(ctx, tx, repoID); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	var n int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM edges WHERE edge_type = $1 AND repo_id = $2`, EdgeTypeTestsSource, repoID).Scan(&n); err != nil {
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
// Caution: the equivalent protocol violation (a statement issued while a cursor from the same Tx is
// open) is NOT transient at all, and pgx names it distinctly enough — "conn busy" — that it can be
// left unmatched here, which the database/sql stack did not allow. A persistent failure that does
// match one of these branches should still be read as a bug in the statement sequence before it is
// read as an infrastructure problem.
func isTransientConnError(err error) bool {
	if err == nil {
		return false
	}
	// driver.ErrBadConn and its text can no longer be produced now that this store speaks pgx
	// directly rather than through database/sql. They are kept because they cost nothing and
	// because the historical note above is only legible if the thing it describes is still named.
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
	// NOT matching "conn busy": that is the protocol violation described in the INVARIANT below, it
	// is perfectly deterministic, and retrying it only adds latency to a guaranteed failure.
	if strings.Contains(msg, "conn closed") || strings.Contains(msg, "unexpected EOF") {
		return true
	}
	return false
}

// testClassRow is one candidate test class read from the cursor before any write happens.
type testClassRow struct {
	id string
	fq string
}

// insertTestsSourceFromNamingConvention adds test→SUT edges by naming convention:
// FooTest / FooIT / FooTests in package P → P.Foo.
//
// INVARIANT: no statement may be issued on tx while a pgx.Rows opened from that same tx is still
// open. A Tx is pinned to a single connection, and that connection cannot carry a second
// query while a result set is being read — native pgx returns "conn busy", and the database/sql
// stack this store used to run on returned "driver: bad connection", which read like an
// infrastructure fault and is not one.
//
// This is not hypothetical. The original implementation ran a per-row SELECT and INSERT inside the
// rows.Next() loop, so materialization failed on the very first test class and rolled the whole
// transaction back, taking the calls/imports insert with it. TESTS_SOURCE edges therefore did not
// exist on any corpus, and gap ranking silently lost its strongest "already covered by tests"
// penalty (retrieval.ListGaps subtracts 38 for an inbound trace edge). Every mock-based test passed
// throughout; only a live database shows it.
//
// `defer rows.Close()` does NOT satisfy the invariant: the deferred close runs when the function
// returns, which is after the later statements, not before them. The three phases below are
// strictly ordered for that reason — drain and close the cursor, then resolve, then write.
func insertTestsSourceFromNamingConvention(ctx context.Context, tx pgx.Tx, repoID string) error {
	// Phase 1 — read the cursor to completion and close it. Nothing else may touch tx until this
	// returns.
	testClasses, err := listTestClasses(ctx, tx, repoID)
	if err != nil {
		return err
	}
	if len(testClasses) == 0 {
		return nil
	}

	// UnderTestClassFQNameFromTestClassFQ stays the single source of truth for the suffix rule. It
	// is unit-tested and has edge cases that are easy to lose in a translation to SQL — a class
	// literally named `Test` maps to nothing, not to an empty simple name.
	sutFQByTestID := make(map[string]string, len(testClasses))
	seenFQ := make(map[string]bool, len(testClasses))
	wantFQ := make([]string, 0, len(testClasses))
	for _, tc := range testClasses {
		sutFQ := UnderTestClassFQNameFromTestClassFQ(tc.fq)
		if sutFQ == "" {
			continue
		}
		sutFQByTestID[tc.id] = sutFQ
		if !seenFQ[sutFQ] {
			seenFQ[sutFQ] = true
			wantFQ = append(wantFQ, sutFQ)
		}
	}
	if len(wantFQ) == 0 {
		return nil
	}

	// Phase 2 — resolve every SUT in one round trip. This was one query per test class, executed
	// inside the cursor loop; on a repo with 200 test classes that is 200 sequential round trips
	// inside a transaction.
	sutIDByFQ, err := resolveProductionClassIDs(ctx, tx, repoID, wantFQ)
	if err != nil {
		return err
	}
	if len(sutIDByFQ) == 0 {
		return nil
	}

	// Phase 3 — write. Dedupe first: the edges primary key is
	// (caller_symbol_id, callee_symbol_id, edge_type), and emitting a pair twice in one statement
	// relies on ON CONFLICT semantics we do not need to lean on.
	callerIDs := make([]string, 0, len(testClasses))
	calleeIDs := make([]string, 0, len(testClasses))
	seenPair := make(map[string]bool, len(testClasses))
	for _, tc := range testClasses {
		sutFQ, ok := sutFQByTestID[tc.id]
		if !ok {
			continue
		}
		sutID, ok := sutIDByFQ[sutFQ]
		if !ok || sutID == tc.id {
			continue
		}
		pair := tc.id + "\x00" + sutID
		if seenPair[pair] {
			continue
		}
		seenPair[pair] = true
		callerIDs = append(callerIDs, tc.id)
		calleeIDs = append(calleeIDs, sutID)
	}
	if len(callerIDs) == 0 {
		return nil
	}
	return insertTestsSourceEdgePairs(ctx, tx, repoID, callerIDs, calleeIDs)
}

// listTestClasses returns every class symbol declared in a test file. The cursor is fully drained
// and closed before returning — see the INVARIANT on insertTestsSourceFromNamingConvention.
func listTestClasses(ctx context.Context, tx pgx.Tx, repoID string) ([]testClassRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.fq_name
		FROM symbols s
		INNER JOIN files f ON f.file = s.file AND f.repo_id = s.repo_id
		WHERE s.repo_id = $1 AND f.is_test = TRUE AND LOWER(s.kind) = 'class'
		ORDER BY s.fq_name, s.file, s.start_line, s.id`, repoID)
	if err != nil {
		return nil, fmt.Errorf("metadata: list test classes: %w", err)
	}
	defer rows.Close()

	var out []testClassRow
	for rows.Next() {
		var r testClassRow
		if err := rows.Scan(&r.id, &r.fq); err != nil {
			return nil, fmt.Errorf("metadata: scan test class: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: list test classes: %w", err)
	}
	return out, nil
}

// resolveProductionClassIDs maps each requested FQ name to the id of the non-test class declaring
// it, in one round trip.
//
// DISTINCT ON with a matching leading ORDER BY key is "first row per fq_name" under a TOTAL order.
// The previous per-class query used `LIMIT 1` with no ORDER BY at all, so which symbol won was
// whatever the executor emitted first — the same defect class as the SearchLexical tie-break and
// the pre-B05 gap ordering, both of which produced irreproducible results.
func resolveProductionClassIDs(ctx context.Context, tx pgx.Tx, repoID string, fqNames []string) (map[string]string, error) {
	// The generic marker is stripped before matching: the naming convention derives "P.Repo" from
	// "P.RepoTests", while the declared symbol is stored as "P.Repo<T>" since B25. Class FQNames
	// contain no '#', so "from the first '<' to the end" is exact; non-generic classes (and every
	// Java/TS class — no '<' can occur) match byte-for-byte as before.
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (bare) bare, id FROM (
			SELECT regexp_replace(s.fq_name, '<.*$', '') AS bare, s.id::text AS id,
			       s.file AS file, s.start_line AS start_line
			FROM symbols s
			INNER JOIN files f ON f.file = s.file AND f.repo_id = s.repo_id
			WHERE s.repo_id = $2
			  AND regexp_replace(s.fq_name, '<.*$', '') = ANY($1::text[])
			  AND LOWER(s.kind) = 'class'
			  AND f.is_test = FALSE
		) t
		ORDER BY bare, file, start_line, id`, fqNames, repoID)
	if err != nil {
		return nil, fmt.Errorf("metadata: resolve production classes: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, len(fqNames))
	for rows.Next() {
		var fq, id string
		if err := rows.Scan(&fq, &id); err != nil {
			return nil, fmt.Errorf("metadata: scan production class: %w", err)
		}
		out[fq] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metadata: resolve production classes: %w", err)
	}
	return out, nil
}

// insertTestsSourceEdgePairs writes all naming-convention edges in one statement.
//
// unnest over two arrays keeps this at three bind parameters regardless of corpus size, so there is
// no placeholder budget to chunk against (Postgres caps a statement at 65535 parameters).
func insertTestsSourceEdgePairs(ctx context.Context, tx pgx.Tx, repoID string, callerIDs, calleeIDs []string) error {
	if len(callerIDs) != len(calleeIDs) {
		return fmt.Errorf("metadata: TESTS_SOURCE pair arrays differ in length (%d vs %d)", len(callerIDs), len(calleeIDs))
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type, repo_id)
		SELECT t.caller::uuid, t.callee::uuid, $3, $4
		FROM unnest($1::text[], $2::text[]) AS t(caller, callee)
		ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO NOTHING`,
		callerIDs, calleeIDs, EdgeTypeTestsSource, repoID)
	if err != nil {
		return fmt.Errorf("metadata: insert TESTS_SOURCE from naming convention: %w", err)
	}
	return nil
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
