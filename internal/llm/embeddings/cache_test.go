package llembed

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakeCache struct {
	store    map[string][]float32
	getCalls int
	putCalls int
	getErr   error
	putErr   error
}

func newFakeCache() *fakeCache { return &fakeCache{store: map[string][]float32{}} }

func (c *fakeCache) GetCachedEmbeddings(ctx context.Context, keys [][]byte) (map[string][]float32, error) {
	c.getCalls++
	if c.getErr != nil {
		return nil, c.getErr
	}
	out := map[string][]float32{}
	for _, k := range keys {
		h := fmt.Sprintf("%x", k)
		if v, ok := c.store[h]; ok {
			out[h] = v
		}
	}
	return out, nil
}

func (c *fakeCache) PutCachedEmbeddings(ctx context.Context, keys [][]byte, vecs [][]float32) error {
	c.putCalls++
	if c.putErr != nil {
		return c.putErr
	}
	for i := range keys {
		c.store[fmt.Sprintf("%x", keys[i])] = vecs[i]
	}
	return nil
}

type countingEmbedder struct {
	calls  int
	inputs []string
	dim    int
	err    error
}

func (e *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.inputs = append(e.inputs, texts...)
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, e.dim)
		v[0] = float32(len(texts[i]))
		out[i] = v
	}
	return out, nil
}

func testKey(provider, model string, dim int, content string) []byte {
	return []byte(provider + "|" + model + "|" + fmt.Sprint(dim) + "|" + content)
}

func TestCachingEmbedder_secondCallHitsCache(t *testing.T) {
	inner := &countingEmbedder{dim: 4}
	cache := newFakeCache()
	e := NewCachingEmbedder(inner, cache, "openai", "text-embedding-3-small", 4, testKey)

	texts := []string{"alpha", "beta"}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("first call should reach the provider once, got %d", inner.calls)
	}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Errorf("second call re-embedded; provider calls = %d, want 1", inner.calls)
	}
}

// The realistic incremental case: one changed chunk in a file of many. Only the changed one should
// reach the provider — that is the whole saving.
func TestCachingEmbedder_onlyMissesReachTheProvider(t *testing.T) {
	inner := &countingEmbedder{dim: 4}
	cache := newFakeCache()
	e := NewCachingEmbedder(inner, cache, "openai", "m", 4, testKey)

	warm := []string{"chunk1", "chunk2", "chunk3"}
	if _, err := e.Embed(context.Background(), warm); err != nil {
		t.Fatal(err)
	}
	inner.inputs = nil

	// chunk2 edited; the rest are byte-identical.
	next := []string{"chunk1", "chunk2-modified", "chunk3"}
	got, err := e.Embed(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.inputs) != 1 || inner.inputs[0] != "chunk2-modified" {
		t.Errorf("provider received %v, want only the changed chunk", inner.inputs)
	}
	// Results must still be in input order, with cached and fresh vectors interleaved correctly.
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	for i, v := range got {
		if len(v) != 4 {
			t.Errorf("vector %d has dimension %d", i, len(v))
		}
	}
	if got[1][0] != float32(len("chunk2-modified")) {
		t.Errorf("the fresh vector landed at the wrong index: %v", got)
	}
}

// TestCachingEmbedder_modelChangeIsACleanMiss is the one real hazard in the design: keyed on
// content alone, a model switch would serve vectors from the previous model — right shape, wrong
// space, undetectable at retrieval time.
func TestCachingEmbedder_modelChangeIsACleanMiss(t *testing.T) {
	cache := newFakeCache()
	innerA := &countingEmbedder{dim: 4}
	a := NewCachingEmbedder(innerA, cache, "openai", "text-embedding-3-small", 4, testKey)
	if _, err := a.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}

	innerB := &countingEmbedder{dim: 4}
	b := NewCachingEmbedder(innerB, cache, "openai", "text-embedding-3-large", 4, testKey)
	if _, err := b.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if innerB.calls != 1 {
		t.Error("a model change must be a cache miss, not a silent reuse of the previous model's vectors")
	}

	// Same for a dimension change.
	innerC := &countingEmbedder{dim: 8}
	c := NewCachingEmbedder(innerC, cache, "openai", "text-embedding-3-small", 8, testKey)
	if _, err := c.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if innerC.calls != 1 {
		t.Error("a dimension change must be a cache miss")
	}
}

// Every cache failure path must degrade to "embed it again", which is exactly the pre-cache
// behaviour — that is what makes the cache safe to enable by default.
func TestCachingEmbedder_cacheFailuresDegradeToEmbedding(t *testing.T) {
	t.Run("lookup error", func(t *testing.T) {
		inner := &countingEmbedder{dim: 4}
		cache := newFakeCache()
		cache.getErr = errors.New("db down")
		e := NewCachingEmbedder(inner, cache, "p", "m", 4, testKey)
		got, err := e.Embed(context.Background(), []string{"a"})
		if err != nil || len(got) != 1 {
			t.Fatalf("a cache lookup failure must not fail the embed: err=%v got=%v", err, got)
		}
	})
	t.Run("write error", func(t *testing.T) {
		inner := &countingEmbedder{dim: 4}
		cache := newFakeCache()
		cache.putErr = errors.New("disk full")
		e := NewCachingEmbedder(inner, cache, "p", "m", 4, testKey)
		if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
			t.Fatalf("a cache write failure must not fail the embed: %v", err)
		}
	})
}

// A provider error must surface, not be masked by the cache layer.
func TestCachingEmbedder_providerErrorPropagates(t *testing.T) {
	sentinel := errors.New("rate limited")
	inner := &countingEmbedder{dim: 4, err: sentinel}
	e := NewCachingEmbedder(inner, newFakeCache(), "p", "m", 4, testKey)
	if _, err := e.Embed(context.Background(), []string{"a"}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the provider error", err)
	}
}

func TestNewCachingEmbedder_disabledConfigurations(t *testing.T) {
	inner := &countingEmbedder{dim: 4}
	if got := NewCachingEmbedder(inner, nil, "p", "m", 4, testKey); got != model2Embedder(inner) {
		t.Error("no cache configured should return the inner embedder unchanged")
	}
	if got := NewCachingEmbedder(inner, newFakeCache(), "p", "m", 0, testKey); got != model2Embedder(inner) {
		t.Error("dimension 0 should return the inner embedder unchanged")
	}
	if got := NewCachingEmbedder(nil, newFakeCache(), "p", "m", 4, testKey); got != nil {
		t.Error("nil inner should return nil")
	}
}

// model2Embedder is an identity helper so the comparisons above read clearly.
func model2Embedder(e *countingEmbedder) interface {
	Embed(context.Context, []string) ([][]float32, error)
} {
	return e
}

func TestCachingEmbedder_emptyInput(t *testing.T) {
	inner := &countingEmbedder{dim: 4}
	e := NewCachingEmbedder(inner, newFakeCache(), "p", "m", 4, testKey)
	got, err := e.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Errorf("empty input: got=%v err=%v", got, err)
	}
	if inner.calls != 0 {
		t.Error("empty input should not reach the provider")
	}
}
