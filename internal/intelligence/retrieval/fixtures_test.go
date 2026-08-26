package retrieval

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

func TestPathProximity(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"src/main/java/com/acme", "src/main/java/com/acme", 0},
		{"src/main/java/com/acme", "src/main/java/com", 1},
		{"src/main/java/com/acme", "src/test/resources", 6}, // 4 up to src/, then 2 down
		{"a", "b", 2},
		{"", "anything", 1},
	}
	for _, c := range cases {
		if got := pathProximity(c.a, c.b); got != c.want {
			t.Errorf("pathProximity(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	// Symmetric.
	if pathProximity("x/y", "x/z/w") != pathProximity("x/z/w", "x/y") {
		t.Error("pathProximity should be symmetric")
	}
}

// configReader returns a fixed candidate set regardless of the query, standing in for the
// alphabetical listing the config path draws from.
type configReader struct{ chunks []embeddings.Chunk }

func (r *configReader) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	return r.chunks, nil
}
func (r *configReader) Search(ctx context.Context, q []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	return nil, nil
}

// TestConfigChunksByPathProximity_prefersNearbyConfig is the behavioural half of the config fix.
// Alphabetical order would put `aaa/` first; proximity puts the config next to the target first.
func TestConfigChunksByPathProximity_prefersNearbyConfig(t *testing.T) {
	r := &configReader{chunks: []embeddings.Chunk{
		{ID: "1", File: "aaa/global/application-config.yml", Lang: "java", Content: "x"},
		{ID: "2", File: "src/main/java/com/acme/config/AcmeConfig.java", Lang: "java", Content: "x"},
		{ID: "3", File: "zzz/other/spring-context.xml", Lang: "java", Content: "x"},
	}}
	target := &embeddings.Chunk{File: "src/main/java/com/acme/OrderService.java", Lang: "java"}

	got := configChunksByPathProximity(context.Background(), r, target, "repo", "java",
		[]string{"config", "context", "spring"}, 2)

	if len(got) == 0 {
		t.Fatal("no config chunks returned")
	}
	if got[0].File != "src/main/java/com/acme/config/AcmeConfig.java" {
		t.Errorf("first config chunk = %q; the nearest config to the target should win, not the "+
			"alphabetically first one", got[0].File)
	}
}

func TestConfigChunksByPathProximity_respectsLimit(t *testing.T) {
	r := &configReader{chunks: []embeddings.Chunk{
		{ID: "1", File: "a/config.yml", Lang: "java", Content: "x"},
		{ID: "2", File: "b/config.yml", Lang: "java", Content: "x"},
		{ID: "3", File: "c/config.yml", Lang: "java", Content: "x"},
	}}
	got := configChunksByPathProximity(context.Background(), r,
		&embeddings.Chunk{File: "a/Service.java"}, "repo", "java", []string{"config"}, 2)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want the limit of 2", len(got))
	}
}

func TestConfigChunksByPathProximity_noTargetFallsBackGracefully(t *testing.T) {
	r := &configReader{chunks: []embeddings.Chunk{{ID: "1", File: "a/config.yml", Lang: "java", Content: "x"}}}
	got := configChunksByPathProximity(context.Background(), r, nil, "repo", "java", []string{"config"}, 5)
	if len(got) != 1 {
		t.Errorf("a nil target should still return candidates, got %d", len(got))
	}
	if got := configChunksByPathProximity(context.Background(), r, nil, "repo", "java", []string{"config"}, 0); got != nil {
		t.Error("a zero limit should return nothing")
	}
}

// rankedReader implements the optional ranked path search so the fixture path can be exercised.
type rankedReader struct {
	configReader
	ranked []embeddings.SearchResult
	called bool
}

func (r *rankedReader) SearchByPathPattern(ctx context.Context, q []float32, opts embeddings.SearchOptions, subs []string) ([]embeddings.SearchResult, error) {
	r.called = true
	return r.ranked, nil
}

func TestRelevantChunksByPathPattern_usesRankedSearchWhenAvailable(t *testing.T) {
	r := &rankedReader{
		configReader: configReader{chunks: []embeddings.Chunk{{ID: "alpha", File: "aaa/fixture.java", Content: "x"}}},
		ranked: []embeddings.SearchResult{
			{Chunk: embeddings.Chunk{ID: "relevant", File: "src/test/resources/fixtures/OrderFixture.java", Content: "x"}},
		},
	}
	target := &embeddings.Chunk{File: "src/main/java/OrderService.java", Embedding: []float32{1, 0}}

	got := relevantChunksByPathPattern(context.Background(), r, target, "repo", "java", []string{"fixture"}, 5)
	if !r.called {
		t.Fatal("ranked search was available but not used")
	}
	if len(got) != 1 || got[0].ID != "relevant" {
		t.Errorf("got %+v, want the ranked result", got)
	}
}

// Without an embedding there is nothing to rank by, so the previous listing behaviour must remain.
func TestRelevantChunksByPathPattern_fallsBackWithoutEmbedding(t *testing.T) {
	r := &rankedReader{
		configReader: configReader{chunks: []embeddings.Chunk{{ID: "alpha", File: "aaa/fixture.java", Lang: "java", Content: "x"}}},
	}
	got := relevantChunksByPathPattern(context.Background(), r,
		&embeddings.Chunk{File: "x.java"}, "repo", "java", []string{"fixture"}, 5)
	if r.called {
		t.Error("ranked search should not be attempted without a target embedding")
	}
	if len(got) != 1 {
		t.Errorf("fallback listing should still return candidates, got %d", len(got))
	}
}
