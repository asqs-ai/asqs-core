package embeddings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
)

// CacheKey derives the cache key for one embedding input.
//
// Provider, model AND dimension are all part of the key. That is the one hazard in this design: key
// on content alone and a model switch silently serves vectors from the previous model, which is
// undetectable at retrieval time — the vectors are the right shape and the wrong space. Including
// all three makes a configuration change a clean miss instead.
func CacheKey(provider, model string, dim int, content string) []byte {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], uint64(dim))
	h.Write(d[:])
	h.Write([]byte{0})
	h.Write([]byte(content))
	return h.Sum(nil)
}

// GetCachedEmbeddings returns the cached vectors for the given keys, keyed by hex key.
// Missing keys are simply absent from the result.
func (s *Store) GetCachedEmbeddings(ctx context.Context, keys [][]byte) (map[string][]float32, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT content_hash, embedding FROM embedding_cache WHERE content_hash = ANY($1)`, keys)
	if err != nil {
		// A cache miss is the pre-existing behaviour, so a cache failure must never fail an index
		// run — it only costs money and time.
		return nil, nil
	}
	defer rows.Close()

	out := make(map[string][]float32, len(keys))
	var hits [][]byte
	for rows.Next() {
		var hash []byte
		var vec pgvector.Vector
		if err := rows.Scan(&hash, &vec); err != nil {
			return out, nil
		}
		out[fmt.Sprintf("%x", hash)] = vec.Slice()
		hits = append(hits, hash)
	}
	if err := rows.Err(); err != nil {
		return out, nil
	}
	s.touchCache(ctx, hits)
	return out, nil
}

// touchCache refreshes last_used_at for cache hits so LRU pruning keeps the working set.
// Best-effort: a failure here costs nothing but pruning accuracy.
func (s *Store) touchCache(ctx context.Context, keys [][]byte) {
	if len(keys) == 0 {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE embedding_cache SET last_used_at = NOW() WHERE content_hash = ANY($1)`, keys)
}

// PutCachedEmbeddings stores vectors for the given keys. Existing rows are left in place (the
// vector for a given key is by definition the same).
func (s *Store) PutCachedEmbeddings(ctx context.Context, keys [][]byte, vecs [][]float32) error {
	if len(keys) == 0 || len(keys) != len(vecs) {
		return nil
	}
	batch := make([][]any, 0, len(keys))
	for i := range keys {
		if len(vecs[i]) != s.dim {
			continue // never cache a vector of the wrong dimension
		}
		batch = append(batch, []any{keys[i], pgvector.NewVector(vecs[i])})
	}
	for _, row := range batch {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO embedding_cache (content_hash, embedding) VALUES ($1, $2)
			 ON CONFLICT (content_hash) DO UPDATE SET last_used_at = NOW()`,
			row[0], row[1]); err != nil {
			// Writing the cache is an optimization; a failure must not fail the run.
			return nil
		}
	}
	return nil
}

// PruneEmbeddingCache deletes cache rows unused for longer than maxAge, returning the count.
//
// Pruning is not optional at scale: a cached vector is ~6 KB, so 1M cached chunks is ~6 GB.
func (s *Store) PruneEmbeddingCache(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM embedding_cache WHERE last_used_at < NOW() - $1::interval`,
		fmt.Sprintf("%d seconds", int(maxAge.Seconds())))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
