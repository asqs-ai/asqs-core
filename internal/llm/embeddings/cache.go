package llembed

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// EmbeddingCache is the storage side of the memo. *embeddings.Store satisfies it.
type EmbeddingCache interface {
	GetCachedEmbeddings(ctx context.Context, keys [][]byte) (map[string][]float32, error)
	PutCachedEmbeddings(ctx context.Context, keys [][]byte, vecs [][]float32) error
}

// KeyFunc derives a cache key from provider, model, dimension and content.
type KeyFunc func(provider, model string, dim int, content string) []byte

// CachingEmbedder memoizes embeddings by content hash.
//
// It wraps the *normalized* embedder, so cached vectors are already unit-length and a cache hit is
// indistinguishable from a fresh embed. Ordering matters: caching outside normalization would store
// raw vectors and re-normalize on every hit; caching inside it would store un-normalized vectors
// that later reads would use directly.
//
// A cache miss is exactly the previous behaviour, which is what makes this safe to enable by
// default: every failure path — lookup error, write error, dimension mismatch — degrades to
// "embed it again".
type CachingEmbedder struct {
	Inner    model.Embedder
	Cache    EmbeddingCache
	Provider string
	Model    string
	Dim      int
	Key      KeyFunc
}

var (
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
)

// CacheHits / CacheMisses report the process-wide memo hit rate. The plan's expectation is a high
// hit rate on the incremental path — a typical PR touches a handful of files, and within those most
// chunks are byte-identical — but the real figure is workload-dependent, so it is measured rather
// than assumed.
func CacheHits() int64   { return cacheHits.Load() }
func CacheMisses() int64 { return cacheMisses.Load() }

// NewCachingEmbedder wraps inner. Returns inner unchanged when no cache is configured.
func NewCachingEmbedder(inner model.Embedder, cache EmbeddingCache, provider, modelName string, dim int, key KeyFunc) model.Embedder {
	if inner == nil || cache == nil || key == nil || dim <= 0 {
		return inner
	}
	return &CachingEmbedder{Inner: inner, Cache: cache, Provider: provider, Model: modelName, Dim: dim, Key: key}
}

// Embed implements model.Embedder, returning cached vectors where available and embedding only the
// misses.
func (e *CachingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	keys := make([][]byte, len(texts))
	hexKeys := make([]string, len(texts))
	for i, t := range texts {
		keys[i] = e.Key(e.Provider, e.Model, e.Dim, t)
		hexKeys[i] = fmt.Sprintf("%x", keys[i])
	}

	cached, _ := e.Cache.GetCachedEmbeddings(ctx, keys)

	out := make([][]float32, len(texts))
	var missIdx []int
	var missTexts []string
	for i := range texts {
		if v, ok := cached[hexKeys[i]]; ok && len(v) == e.Dim {
			out[i] = v
			cacheHits.Add(1)
			continue
		}
		cacheMisses.Add(1)
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, texts[i])
	}
	if len(missTexts) == 0 {
		return out, nil
	}

	fresh, err := e.Inner.Embed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	if len(fresh) != len(missTexts) {
		return nil, fmt.Errorf("embeddings: provider returned %d vectors for %d inputs", len(fresh), len(missTexts))
	}

	storeKeys := make([][]byte, 0, len(missIdx))
	storeVecs := make([][]float32, 0, len(missIdx))
	for j, idx := range missIdx {
		out[idx] = fresh[j]
		storeKeys = append(storeKeys, keys[idx])
		storeVecs = append(storeVecs, fresh[j])
	}
	// Best-effort: a write failure costs money on the next run, not correctness on this one.
	_ = e.Cache.PutCachedEmbeddings(ctx, storeKeys, storeVecs)
	return out, nil
}
