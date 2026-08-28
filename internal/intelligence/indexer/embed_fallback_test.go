package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type limitByBatchAndSizeEmbedder struct {
	dim       int
	maxBatch  int
	maxRunes  int
	callSizes []int
}

func (e *limitByBatchAndSizeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.callSizes = append(e.callSizes, len(texts))
	if e.maxBatch > 0 && len(texts) > e.maxBatch {
		return nil, fmt.Errorf("status code: 413 payload too large")
	}
	for _, t := range texts {
		if e.maxRunes > 0 && len([]rune(t)) > e.maxRunes {
			return nil, fmt.Errorf("maximum input length is 8192 tokens")
		}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, e.dim)
	}
	return out, nil
}

func TestEmbedChunksWithFallback_splitsBatchOnProviderLimit(t *testing.T) {
	embedder := &limitByBatchAndSizeEmbedder{dim: 8, maxBatch: 2, maxRunes: 1000}
	var in []*ChunkToEmbed
	for i := 0; i < 5; i++ {
		in = append(in, &ChunkToEmbed{
			Content:   fmt.Sprintf("chunk-%d", i),
			File:      "a/Foo.cs",
			Lang:      "csharp",
			StartLine: i + 1,
			EndLine:   i + 1,
			RepoID:    "r",
		})
	}
	stats := &embedFallbackStats{}
	got, dim, err := embedChunksWithFallback(context.Background(), embedder, in, DefaultChunkConfig(), stats)
	if err != nil {
		t.Fatalf("embedChunksWithFallback: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("chunks = %d; want %d", len(got), len(in))
	}
	if dim != 8 {
		t.Fatalf("dim = %d; want 8", dim)
	}
	if !stats.FileTooLarge {
		t.Fatal("expected FileTooLarge=true")
	}
	if len(embedder.callSizes) < 3 {
		t.Fatalf("expected multiple adaptive embed calls; got %v", embedder.callSizes)
	}
}

func TestEmbedChunksWithFallback_splitsSingleChunkWhenTooLarge(t *testing.T) {
	embedder := &limitByBatchAndSizeEmbedder{dim: 4, maxBatch: 10, maxRunes: 120}
	content := strings.Repeat("line of code and comments for embedding fallback coverage\n", 20)
	in := []*ChunkToEmbed{{
		Content:      content,
		File:         "src/BigService.ts",
		Lang:         "typescript",
		StartLine:    10,
		EndLine:      200,
		RepoID:       "r",
		MetadataJSON: []byte(`{"symbol_kind":"class","chunk_index":0}`),
	}}
	cfg := DefaultChunkConfig()
	cfg.MaxTokens = 32
	cfg.CharsPerToken = 4

	stats := &embedFallbackStats{}
	got, _, err := embedChunksWithFallback(context.Background(), embedder, in, cfg, stats)
	if err != nil {
		t.Fatalf("embedChunksWithFallback: %v", err)
	}
	if len(got) <= 1 {
		t.Fatalf("expected chunk segmentation; got %d chunk(s)", len(got))
	}
	if stats.SegmentRetries == 0 || stats.SegmentsCreated == 0 {
		t.Fatalf("expected segmentation retries/creation stats, got %#v", stats)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(got[0].MetadataJSON, &meta); err != nil {
		t.Fatalf("segment metadata json: %v", err)
	}
	if _, ok := meta["embedding_segment_index"]; !ok {
		t.Fatalf("expected embedding_segment_index in metadata: %v", meta)
	}
	if got[0].StartLine < in[0].StartLine || got[0].EndLine > in[0].EndLine {
		t.Fatalf("segment line range out of parent bounds: start=%d end=%d parent=%d..%d", got[0].StartLine, got[0].EndLine, in[0].StartLine, in[0].EndLine)
	}
}

func TestEmbedSkipReason_granularBuckets(t *testing.T) {
	t.Parallel()
	wrap := func(inner error) error {
		return fmt.Errorf("%w: %v", errEmbedSkipFile, inner)
	}
	tests := []struct {
		inner error
		want  string
	}{
		{fmt.Errorf("maximum input length is 8192 tokens"), "embedding_provider_limit"},
		{fmt.Errorf("request timeout"), "embedding_timeout"},
		{fmt.Errorf("ollama embeddings: status 429: slow down"), "embedding_rate_limit"},
		{fmt.Errorf("rate limit exceeded"), "embedding_rate_limit"},
		{fmt.Errorf("ollama embeddings: status 503: "), "embedding_upstream_http"},
		{fmt.Errorf("connection reset by peer"), "embedding_network"},
		{fmt.Errorf("503 Service Unavailable"), "recoverable_api_error"},
	}
	for _, tt := range tests {
		if got := embedSkipReason(wrap(tt.inner)); got != tt.want {
			t.Errorf("embedSkipReason(%v) = %q want %q", tt.inner, got, tt.want)
		}
	}
}
