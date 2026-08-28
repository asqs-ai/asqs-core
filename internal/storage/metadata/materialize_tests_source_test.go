package metadata

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsTransientConnError pins the classifier used by the MaterializeTestsSourceEdges
// retry loop. Bad connections are recoverable; constraint violations and syntax errors
// must NOT be retried (they are permanent and would otherwise loop until the attempt
// budget is exhausted).
// matRepo scopes every fixture in this file. The materialization is repo-scoped as of B23, so
// fixtures written without a repo_id would be materialized under the empty repository and prove
// nothing about the scoped path callers actually take.
const matRepo = "materialize/repo"

func TestIsTransientConnError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"driver.ErrBadConn sentinel", driver.ErrBadConn, true},
		{"wrapped driver.ErrBadConn", fmt.Errorf("scan: %w", driver.ErrBadConn), true},
		{"driver: bad connection text", errors.New("driver: bad connection"), true},
		{"connection reset by peer", errors.New("read tcp: connection reset by peer"), true},
		{"broken pipe", errors.New("write tcp: broken pipe"), true},
		{"unique violation", errors.New("pq: duplicate key value violates unique constraint"), false},
		{"syntax error", errors.New(`pq: syntax error at or near "FRO"`), false},
		// pgx-native cases. Without these branches the pgxpool migration would have turned the
		// retry loop into a no-op for the only failure it exists for: a backend restarting or
		// failing over underneath an open transaction.
		{"admin shutdown 57P01", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, true},
		{"crash shutdown 57P02", &pgconn.PgError{Code: "57P02"}, true},
		{"cannot connect now 57P03", &pgconn.PgError{Code: "57P03"}, true},
		{"connection failure 08006", &pgconn.PgError{Code: "08006"}, true},
		{"wrapped connection failure", fmt.Errorf("materialize: %w", &pgconn.PgError{Code: "08006"}), true},
		{"conn closed", errors.New("conn closed"), true},
		{"pg unique violation 23505", &pgconn.PgError{Code: "23505"}, false},
		{"pg syntax error 42601", &pgconn.PgError{Code: "42601"}, false},
		// The protocol violation from the INVARIANT: deterministic, must never be retried.
		{"conn busy", errors.New("conn busy"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientConnError(tc.err); got != tc.want {
				t.Errorf("isTransientConnError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUnderTestClassFQNameFromTestClassFQ(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"com.example.FooTest", "com.example.Foo"},
		{"com.example.FooTests", "com.example.Foo"},
		{"com.example.FooIT", "com.example.Foo"},
		{"com.example.Foo", ""},
		{"FooTest", "Foo"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := UnderTestClassFQNameFromTestClassFQ(tc.in); got != tc.want {
			t.Errorf("UnderTestClassFQNameFromTestClassFQ(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestMaterializeTestsSourceEdges_integration(t *testing.T) {
	conn := os.Getenv("ASQS_TEST_METADATA_URL")
	if conn == "" {
		t.Skip("ASQS_TEST_METADATA_URL not set")
	}
	ctx := context.Background()
	s, err := Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	prefix := "matts-" + time.Now().Format("150405.000000000")
	testFile := prefix + "/FooTest.java"
	prodFile := prefix + "/Foo.java"
	_, _ = s.db.Exec(ctx, `DELETE FROM symbols WHERE file = $1 OR file = $2`, testFile, prodFile)
	_, _ = s.db.Exec(ctx, `DELETE FROM files WHERE file = $1 OR file = $2`, testFile, prodFile)

	if err := s.UpsertFile(ctx, &File{RepoID: matRepo, File: testFile, SHA: "1", Lang: "java", Module: "m", IsTest: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFile(ctx, &File{RepoID: matRepo, File: prodFile, SHA: "2", Lang: "java", Module: "m", IsTest: false}); err != nil {
		t.Fatal(err)
	}
	testClass := &Symbol{RepoID: matRepo, Lang: "java", Kind: "class", FQName: prefix + ".FooTest", File: testFile, StartLine: 1, EndLine: 5}
	prodClass := &Symbol{RepoID: matRepo, Lang: "java", Kind: "class", FQName: prefix + ".Foo", File: prodFile, StartLine: 1, EndLine: 10}
	testMeth := &Symbol{RepoID: matRepo, Lang: "java", Kind: "method", FQName: prefix + ".FooTest#t", File: testFile, StartLine: 3, EndLine: 4}
	prodMeth := &Symbol{RepoID: matRepo, Lang: "java", Kind: "method", FQName: prefix + ".Foo#bar", File: prodFile, StartLine: 5, EndLine: 6}
	tid, err := s.InsertSymbol(ctx, testClass)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := s.InsertSymbol(ctx, prodClass)
	if err != nil {
		t.Fatal(err)
	}
	tmid, err := s.InsertSymbol(ctx, testMeth)
	if err != nil {
		t.Fatal(err)
	}
	pmid, err := s.InsertSymbol(ctx, prodMeth)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.InsertEdge(ctx, &Edge{RepoID: matRepo, CallerSymbolID: tmid, CalleeSymbolID: pmid, EdgeType: "calls"})
	n, err := s.MaterializeTestsSourceEdges(ctx, matRepo)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("materialized count = %d; want >= 1", n)
	}
	var cnt int
	err = s.db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM edges
		WHERE edge_type = $1 AND caller_symbol_id = $2 AND callee_symbol_id = $3`,
		EdgeTypeTestsSource, tmid, pmid).Scan(&cnt)
	if err != nil || cnt != 1 {
		t.Fatalf("call-derived TESTS_SOURCE: cnt=%d err=%v", cnt, err)
	}
	err = s.db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM edges
		WHERE edge_type = $1 AND caller_symbol_id = $2 AND callee_symbol_id = $3`,
		EdgeTypeTestsSource, tid, pid).Scan(&cnt)
	if err != nil || cnt != 1 {
		t.Fatalf("naming-derived TESTS_SOURCE: cnt=%d err=%v", cnt, err)
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM symbols WHERE file = $1 OR file = $2`, testFile, prodFile)
	_, _ = s.db.Exec(ctx, `DELETE FROM files WHERE file = $1 OR file = $2`, testFile, prodFile)
}

// scratchStoreForMaterializeTest opens a store against the scratch database, or skips.
//
// New tests in this file use ScratchDBForTests rather than reading ASQS_TEST_METADATA_URL directly:
// these tests WRITE symbols and edges, and the raw-getenv pattern used above is how fixtures once
// landed in a live corpus.
func scratchStoreForMaterializeTest(t *testing.T) (*Store, context.Context) {
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

// seedTestAndProdClass inserts a (test class, production class) pair under a unique path prefix and
// registers cleanup. Returns the two symbol ids.
func seedTestAndProdClass(t *testing.T, s *Store, ctx context.Context, prefix, simpleTestName, simpleProdName string) (testID, prodID string) {
	t.Helper()
	testFile := prefix + "/" + simpleTestName + ".java"
	prodFile := prefix + "/" + simpleProdName + ".java"
	if err := s.UpsertFile(ctx, &File{RepoID: matRepo, File: testFile, SHA: "1", Lang: "java", Module: "m", IsTest: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFile(ctx, &File{RepoID: matRepo, File: prodFile, SHA: "2", Lang: "java", Module: "m", IsTest: false}); err != nil {
		t.Fatal(err)
	}
	testID, err := s.InsertSymbol(ctx, &Symbol{RepoID: matRepo, Lang: "java", Kind: "class", FQName: prefix + "." + simpleTestName, File: testFile, StartLine: 1, EndLine: 5})
	if err != nil {
		t.Fatal(err)
	}
	prodID, err = s.InsertSymbol(ctx, &Symbol{RepoID: matRepo, Lang: "java", Kind: "class", FQName: prefix + "." + simpleProdName, File: prodFile, StartLine: 1, EndLine: 9})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(context.Background(), `DELETE FROM symbols WHERE file = $1 OR file = $2`, testFile, prodFile)
		_, _ = s.db.Exec(context.Background(), `DELETE FROM files WHERE file = $1 OR file = $2`, testFile, prodFile)
	})
	return testID, prodID
}

func materializeTestPrefix(t *testing.T) string {
	t.Helper()
	return "matts-" + time.Now().Format("150405.000000000")
}

// TestMaterializeTestsSourceEdges_succeedsOnFirstAttempt is the guard that stops D1 recurring.
//
// The nested-cursor defect surfaced as "driver: bad connection", which isTransientConnError
// classifies as retryable. The retry loop therefore turned a 100%-reproducible bug into a slower
// 100%-reproducible bug whose error message blamed the network, and raising the attempt budget
// looked like progress. A healthy database must complete on attempt ONE: any retry here means the
// statement sequence is wrong, not the pool.
func TestMaterializeTestsSourceEdges_succeedsOnFirstAttempt(t *testing.T) {
	s, ctx := scratchStoreForMaterializeTest(t)
	prefix := materializeTestPrefix(t)
	seedTestAndProdClass(t, s, ctx, prefix, "FirstAttemptTest", "FirstAttempt")

	sleeps := captureSleeps(t)
	if _, err := s.MaterializeTestsSourceEdges(ctx, matRepo); err != nil {
		t.Fatalf("MaterializeTestsSourceEdges against a healthy database: %v", err)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("materialization backed off %d time(s) (%v) against a healthy database; "+
			"a retry here means a deterministic error is being misread as a transient one — "+
			"that is exactly how the nested-cursor defect stayed hidden", len(*sleeps), *sleeps)
	}
}

// TestMaterializeTestsSourceEdges_namingConventionCoversEveryClass covers the batched resolve/write
// path introduced with the cursor fix.
//
// A single-class fixture cannot distinguish "drains the cursor" from "fails on the first row":
// both old and new code produce one edge or zero. Several classes are what proves the loop runs to
// completion and that the batched INSERT emits a row per pair rather than only the first.
func TestMaterializeTestsSourceEdges_namingConventionCoversEveryClass(t *testing.T) {
	s, ctx := scratchStoreForMaterializeTest(t)
	prefix := materializeTestPrefix(t)

	want := map[string]string{} // testID -> prodID
	for _, n := range []struct{ test, prod string }{
		{"AlphaTest", "Alpha"},
		{"BetaTests", "Beta"},
		{"GammaIT", "Gamma"},
	} {
		tid, pid := seedTestAndProdClass(t, s, ctx, prefix, n.test, n.prod)
		want[tid] = pid
	}
	// A test class whose production counterpart does not exist must be skipped silently, not error.
	orphanFile := prefix + "/OrphanTest.java"
	if err := s.UpsertFile(ctx, &File{RepoID: matRepo, File: orphanFile, SHA: "3", Lang: "java", Module: "m", IsTest: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSymbol(ctx, &Symbol{RepoID: matRepo, Lang: "java", Kind: "class", FQName: prefix + ".OrphanTest", File: orphanFile, StartLine: 1, EndLine: 3}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(context.Background(), `DELETE FROM symbols WHERE file = $1`, orphanFile)
		_, _ = s.db.Exec(context.Background(), `DELETE FROM files WHERE file = $1`, orphanFile)
	})

	if _, err := s.MaterializeTestsSourceEdges(ctx, matRepo); err != nil {
		t.Fatalf("MaterializeTestsSourceEdges: %v", err)
	}

	for testID, prodID := range want {
		var cnt int
		if err := s.db.QueryRow(ctx, `
			SELECT COUNT(*)::int FROM edges
			WHERE edge_type = $1 AND caller_symbol_id = $2 AND callee_symbol_id = $3`,
			EdgeTypeTestsSource, testID, prodID).Scan(&cnt); err != nil {
			t.Fatalf("count edge %s->%s: %v", testID, prodID, err)
		}
		if cnt != 1 {
			t.Errorf("naming-convention edge %s->%s: count = %d, want 1; the cursor loop must "+
				"produce an edge for EVERY test class, not just the first", testID, prodID, cnt)
		}
	}
}

// TestMaterializeTestsSourceEdges_isRerunnable guards the DELETE-then-INSERT cycle.
//
// Materialization runs on every index pass, so it must be idempotent: the second run must produce
// the same edge set, not duplicates and not an empty set.
func TestMaterializeTestsSourceEdges_isRerunnable(t *testing.T) {
	s, ctx := scratchStoreForMaterializeTest(t)
	prefix := materializeTestPrefix(t)
	testID, prodID := seedTestAndProdClass(t, s, ctx, prefix, "RerunTest", "Rerun")

	first, err := s.MaterializeTestsSourceEdges(ctx, matRepo)
	if err != nil {
		t.Fatalf("first materialization: %v", err)
	}
	second, err := s.MaterializeTestsSourceEdges(ctx, matRepo)
	if err != nil {
		t.Fatalf("second materialization: %v", err)
	}
	if first != second {
		t.Errorf("edge count changed across identical runs: %d then %d", first, second)
	}
	var cnt int
	if err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM edges
		WHERE edge_type = $1 AND caller_symbol_id = $2 AND callee_symbol_id = $3`,
		EdgeTypeTestsSource, testID, prodID).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("edge count after two runs = %d, want exactly 1", cnt)
	}
}
