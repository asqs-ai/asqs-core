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
func MetadataMigrations() []Migration {
	return nil
}
