package embeddings

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// scratchEmbeddingsURL returns a connection string for a database tests may WRITE to, or "".
//
// Same gate as metadata.ScratchDBForTests and for the same reason: this test deliberately drives
// the branch that runs TRUNCATE TABLE chunks, so pointing it at a real corpus would destroy it.
func scratchEmbeddingsURL(t *testing.T) string {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("ASQS_TEST_METADATA_URL"))
	if url == "" {
		t.Skip("set ASQS_TEST_METADATA_URL to a scratch database to run this")
	}
	name := url
	if i := strings.Index(name, "?"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	low := strings.ToLower(name)
	if !strings.Contains(low, "test") && !strings.Contains(low, "scratch") {
		t.Skipf("refusing to run a TRUNCATE test against database %q: name it *test* or *scratch*", name)
	}
	return url
}

func countChunks(t *testing.T, s *Store, ctx context.Context) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	return n
}

func seedOneChunk(t *testing.T, s *Store, ctx context.Context, dim int) {
	t.Helper()
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = 0.01
	}
	if _, err := s.InsertChunk(ctx, &Chunk{
		Content: "class Foo {}", Embedding: vec, File: "src/Foo.java",
		Lang: "java", ChunkType: "definition", StartLine: 1, EndLine: 3,
		RepoID: "github.com/acme/dimguard",
	}); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
}

// TestInitSchema_refusesToTruncatePopulatedCorpusOnDimChange is the behavioural test the source-text
// guard in dim_guard_test.go stands in for.
//
// The destructive branch is DROP INDEX + TRUNCATE TABLE chunks + ALTER COLUMN, it runs from
// InitSchema on process start, and it hits every repository in the database rather than the one
// being indexed. A source-order assertion cannot prove the refusal actually fires — only running it
// against Postgres can, and this repository ships configs at both 768 and 1536 dimensions, so the
// mistake is one -config flag away.
func TestInitSchema_refusesToTruncatePopulatedCorpusOnDimChange(t *testing.T) {
	url := scratchEmbeddingsURL(t)
	ctx := context.Background()
	t.Setenv(EnvAllowEmbeddingDimReset, "") // explicitly NOT opted in

	small, err := Open(ctx, Config{ConnString: url, Dimension: 768})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	if err := small.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema at 768: %v", err)
	}
	if _, err := small.pool.Exec(ctx, `TRUNCATE TABLE chunks`); err != nil {
		t.Fatalf("reset chunks: %v", err)
	}
	seedOneChunk(t, small, ctx, 768)
	before := countChunks(t, small, ctx)
	if before != 1 {
		t.Fatalf("setup: %d chunks, want 1", before)
	}

	// Same database, different configured dimension: the realistic mistake.
	big, err := Open(ctx, Config{ConnString: url, Dimension: 1536})
	if err != nil {
		t.Fatal(err)
	}
	defer big.Close()

	err = big.InitSchema(ctx)
	if err == nil {
		t.Fatal("InitSchema succeeded against a populated corpus at a different dimension; " +
			"it must refuse, because the only way to succeed is TRUNCATE TABLE chunks")
	}
	if !errors.Is(err, ErrEmbeddingDimMismatch) {
		t.Errorf("error = %v; want it to wrap ErrEmbeddingDimMismatch so callers can classify it", err)
	}
	// The message has to be actionable: it is the only thing an operator sees.
	for _, want := range []string{"768", "1536", EnvAllowEmbeddingDimReset} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not mention %q: %v", want, err)
		}
	}
	if after := countChunks(t, small, ctx); after != before {
		t.Fatalf("corpus changed from %d to %d chunks despite the refusal", before, after)
	}
	if _, err := small.pool.Exec(ctx, `TRUNCATE TABLE chunks`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// An empty table has nothing to lose, so the realign must proceed — otherwise "fresh database,
// changed the model" becomes an unrecoverable state.
func TestInitSchema_realignsFreelyWhenCorpusIsEmpty(t *testing.T) {
	url := scratchEmbeddingsURL(t)
	ctx := context.Background()
	t.Setenv(EnvAllowEmbeddingDimReset, "")

	small, err := Open(ctx, Config{ConnString: url, Dimension: 768})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	if err := small.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema at 768: %v", err)
	}
	if _, err := small.pool.Exec(ctx, `TRUNCATE TABLE chunks`); err != nil {
		t.Fatalf("reset chunks: %v", err)
	}

	big, err := Open(ctx, Config{ConnString: url, Dimension: 1536})
	if err != nil {
		t.Fatal(err)
	}
	defer big.Close()
	if err := big.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema must realign an empty corpus freely, got: %v", err)
	}
	seedOneChunk(t, big, ctx, 1536)
	if n := countChunks(t, big, ctx); n != 1 {
		t.Fatalf("realigned store rejected a %d-dim insert (%d chunks)", 1536, n)
	}
	if _, err := big.pool.Exec(ctx, `TRUNCATE TABLE chunks`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
