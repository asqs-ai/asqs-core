package retrieval

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

func TestNormalizeContextRequestForRetrieveKey_defaultsMatchRetrieve(t *testing.T) {
	a := normalizeContextRequestForRetrieveKey(ContextRequest{
		SymbolID: "s", Lang: "java", RepoID: "r", Profile: ProfileJavaUnit,
		MaxDependencyChunks: 0, MaxSimilarTests: 0, MaxFixtures: 0, SimilarMMRLambda: 0,
	})
	if a.MaxDependencyChunks != 15 || a.MaxSimilarTests != 5 || a.MaxFixtures != 5 || a.MaxConfigChunks != defaultMaxConfigChunks || a.DependencyMaxDepth != defaultDependencyMaxDepth {
		t.Fatalf("defaults: %+v", a)
	}
	if a.SimilarMMRLambda != defaultSimilarMMRLambda {
		t.Fatalf("lambda: got %v want %v", a.SimilarMMRLambda, defaultSimilarMMRLambda)
	}
	b := normalizeContextRequestForRetrieveKey(ContextRequest{
		SymbolID: "s", Lang: "java", RepoID: "r", Profile: ProfileJavaUnit,
		MaxDependencyChunks: 15, MaxSimilarTests: 5, MaxFixtures: 5, MaxConfigChunks: defaultMaxConfigChunks, DependencyMaxDepth: defaultDependencyMaxDepth, SimilarMMRLambda: defaultSimilarMMRLambda,
	})
	if retrievalCacheKey(a) != retrievalCacheKey(b) {
		t.Errorf("zero limits and explicit defaults should produce same cache key")
	}
}

func TestRetrievalCacheKey_differentBudgetsDiffer(t *testing.T) {
	base := ContextRequest{SymbolID: "x", Lang: "java", RepoID: "r", Profile: ProfileJavaUnit, MaxSimilarTests: 10}
	other := base
	other.MaxSimilarTests = 11
	if retrievalCacheKey(base) == retrievalCacheKey(other) {
		t.Error("different MaxSimilarTests should change key")
	}
}

func TestRetrievalCacheKey_maxContextChunksAffectsKey(t *testing.T) {
	a := normalizeContextRequestForRetrieveKey(ContextRequest{
		SymbolID: "s", Lang: "java", RepoID: "r", Profile: ProfileJavaUnit,
		MaxContextChunks: 0,
	})
	b := normalizeContextRequestForRetrieveKey(ContextRequest{
		SymbolID: "s", Lang: "java", RepoID: "r", Profile: ProfileJavaUnit,
		MaxContextChunks: 100,
	})
	if retrievalCacheKey(a) == retrievalCacheKey(b) {
		t.Error("different MaxContextChunks should change cache key")
	}
}

func TestWithinRunRetrieveCache_singleflight(t *testing.T) {
	var calls atomic.Int32
	c := newWithinRunRetrieveCache()
	key := `{"symbol_id":"s"}`
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			_, _ = c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
				calls.Add(1)
				return &RetrievalContext{FailureHint: "h"}, nil
			})
		}()
	}
	close(ready)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("want exactly 1 fn execution, got %d", calls.Load())
	}
	// Dedup accounting (fast path vs coalesce) varies with scheduling; see slowFn test for coalesce.
}

func TestWithinRunRetrieveCache_slowFnCoalescesViaSingleflight(t *testing.T) {
	var calls atomic.Int32
	c := newWithinRunRetrieveCache()
	key := `{"symbol_id":"slow"}`
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			_, _ = c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return &RetrievalContext{FailureHint: "h"}, nil
			})
		}()
	}
	close(ready)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("want exactly 1 fn execution, got %d", calls.Load())
	}
	if c.coalesceHits() < 1 {
		t.Fatalf("slow fn should let waiters coalesce in singleflight, coalesceHits=%d", c.coalesceHits())
	}
}

func TestWithinRunRetrieveCache_mapFastPath(t *testing.T) {
	var calls atomic.Int32
	c := newWithinRunRetrieveCache()
	key := `{"symbol_id":"z"}`
	_, err := c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
		calls.Add(1)
		return &RetrievalContext{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
		calls.Add(1)
		return &RetrievalContext{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("second call should be fast path; fn calls=%d", calls.Load())
	}
	if c.fastPathHits() != 1 {
		t.Fatalf("fastPathHits=%d", c.fastPathHits())
	}
}

func TestWithinRunRetrieveCache_noStoreOnError(t *testing.T) {
	c := newWithinRunRetrieveCache()
	key := `{"symbol_id":"err"}`
	var calls atomic.Int32
	_, _ = c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
		calls.Add(1)
		return nil, errors.New("fail")
	})
	if calls.Load() != 1 {
		t.Fatalf("first call should run fn once")
	}
	_, _ = c.getOrRetrieve(context.Background(), key, func() (*RetrievalContext, error) {
		calls.Add(1)
		return &RetrievalContext{}, nil
	})
	if calls.Load() != 2 {
		t.Fatalf("after failed first store, second attempt should run fn again; calls=%d", calls.Load())
	}
}

// countingChunkReader counts List and Search invocations (embedding I/O during Retrieve).
type countingChunkReader struct {
	inner       ChunkReader
	listCalls   atomic.Int32
	searchCalls atomic.Int32
}

func (c *countingChunkReader) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	c.listCalls.Add(1)
	if c.inner == nil {
		return nil, nil
	}
	return c.inner.List(ctx, opts)
}

func (c *countingChunkReader) Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	c.searchCalls.Add(1)
	if c.inner == nil {
		return nil, nil
	}
	return c.inner.Search(ctx, queryEmbedding, opts)
}

func (c *countingChunkReader) total() int32 {
	return c.listCalls.Load() + c.searchCalls.Load()
}
