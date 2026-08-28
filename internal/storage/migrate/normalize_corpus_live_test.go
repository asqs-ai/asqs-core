package migrate

import (
	"context"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// TestNormalizeChunkEmbeddings_liveCorpus runs the first real embeddings migration against a live
// scratch database (core-own; upstream verified the migration on its corpus manually): a
// denormalized vector is scaled to unit length with its direction preserved, a zero vector is left
// alone rather than divided by zero, and a re-run is recorded as already applied.
func TestNormalizeChunkEmbeddings_liveCorpus(t *testing.T) {
	url, why := metadata.ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()

	// The chunks table comes from the embeddings store's own schema path — the migration must not
	// create it (it normalizes an existing corpus), so the test provisions it the way a run does.
	st, err := embeddings.Open(ctx, embeddings.Config{ConnString: url, Dimension: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InitSchema(ctx); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM chunks WHERE file LIKE 'livetest_norm_%'`)
		// Remove every embeddings-ledger row this run recorded, so the test stays repeatable as
		// the migration list grows.
		for _, m := range EmbeddingsMigrations() {
			_, _ = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE id = $1`, m.ID)
		}
	}()

	const ins = `INSERT INTO chunks (content, embedding, file, lang, chunk_type, start_line, end_line, repo_id)
	             VALUES ($1, $2::vector, $3, 'go', 'test', 1, 2, 'livetest')`
	if _, err := pool.Exec(ctx, ins, "denormalized", "[3,4,0,0,0,0,0,0]", "livetest_norm_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, "zero", "[0,0,0,0,0,0,0,0]", "livetest_norm_zero"); err != nil {
		t.Fatal(err)
	}

	res, err := Run(ctx, pool, EmbeddingsMigrations(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	applied := map[string]bool{}
	for _, id := range res.Applied {
		applied[id] = true
	}
	if !applied["0001_normalize_chunk_embeddings"] {
		t.Fatalf("normalize migration not applied: %v", res.Applied)
	}

	var norm float64
	var dirDist float64
	err = pool.QueryRow(ctx, `SELECT sqrt(inner_product(embedding, embedding)),
	                                 embedding <-> '[0.6,0.8,0,0,0,0,0,0]'::vector
	                          FROM chunks WHERE file = 'livetest_norm_a'`).Scan(&norm, &dirDist)
	if err != nil {
		t.Fatal(err)
	}
	if norm < 1-1e-6 || norm > 1+1e-6 {
		t.Errorf("normalized vector has norm %v, want 1", norm)
	}
	if dirDist > 1e-6 {
		t.Errorf("direction not preserved: distance to expected unit vector = %v", dirDist)
	}

	err = pool.QueryRow(ctx, `SELECT sqrt(inner_product(embedding, embedding))
	                          FROM chunks WHERE file = 'livetest_norm_zero'`).Scan(&norm)
	if err != nil {
		t.Fatal(err)
	}
	if norm != 0 {
		t.Errorf("zero vector was modified: norm %v", norm)
	}

	again, err := Run(ctx, pool, EmbeddingsMigrations(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Applied) != 0 || len(again.Skipped) != len(EmbeddingsMigrations()) {
		t.Errorf("re-run: applied=%v skipped=%v, want all skipped", again.Applied, again.Skipped)
	}
}
