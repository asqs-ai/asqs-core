package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

const (
	minEmbedSegmentRunes     = 32
	defaultEmbedSegmentRunes = 3200
	defaultEmbedOverlapRunes = 160
)

var errEmbedSkipFile = fmt.Errorf("indexer: skip file embeddings")

type embedFallbackStats struct {
	FileTooLarge    bool
	SegmentRetries  int
	SegmentsCreated int
	SegmentsDropped int
}

func embedChunksWithFallback(ctx context.Context, embedder Embedder, in []*ChunkToEmbed, cfg ChunkConfig, stats *embedFallbackStats) ([]*embeddings.Chunk, int, error) {
	if len(in) == 0 {
		return nil, 0, nil
	}
	if stats == nil {
		stats = &embedFallbackStats{}
	}
	maxRunes := maxSegmentRunes(cfg)
	overlapRunes := overlapRunes(cfg, maxRunes)
	return embedChunksAdaptive(ctx, embedder, in, maxRunes, overlapRunes, stats)
}

func embedChunksAdaptive(ctx context.Context, embedder Embedder, in []*ChunkToEmbed, maxRunes, overlapRunes int, stats *embedFallbackStats) ([]*embeddings.Chunk, int, error) {
	if len(in) == 0 {
		return nil, 0, nil
	}
	texts := make([]string, len(in))
	for i := range in {
		texts[i] = in[i].Content
	}
	vecs, err := embedder.Embed(ctx, texts)
	if err == nil {
		if len(vecs) != len(in) {
			return nil, 0, fmt.Errorf("indexer: embed returned %d vectors for %d chunks", len(vecs), len(in))
		}
		out := make([]*embeddings.Chunk, len(in))
		dim := 0
		for i := range in {
			out[i] = in[i].ToChunk(vecs[i])
			if dim == 0 {
				dim = len(vecs[i])
			}
		}
		return out, dim, nil
	}
	if IsEmbeddingProviderLimitError(err) {
		stats.FileTooLarge = true
		if len(in) > 1 {
			mid := len(in) / 2
			left, leftDim, leftErr := embedChunksAdaptive(ctx, embedder, in[:mid], maxRunes, overlapRunes, stats)
			if leftErr != nil {
				return nil, 0, leftErr
			}
			right, rightDim, rightErr := embedChunksAdaptive(ctx, embedder, in[mid:], maxRunes, overlapRunes, stats)
			if rightErr != nil {
				return nil, 0, rightErr
			}
			dim := leftDim
			if dim == 0 {
				dim = rightDim
			}
			return append(left, right...), dim, nil
		}
		curLen := len([]rune(in[0].Content))
		splitTarget := maxRunes
		if splitTarget >= curLen {
			splitTarget = curLen / 2
		}
		if splitTarget < minEmbedSegmentRunes {
			splitTarget = minEmbedSegmentRunes
		}
		if splitTarget >= curLen {
			stats.SegmentsDropped++
			return nil, 0, nil
		}
		segmented := splitChunkForEmbedding(in[0], splitTarget, overlapRunes)
		if len(segmented) <= 1 {
			stats.SegmentsDropped++
			return nil, 0, nil
		}
		stats.SegmentRetries++
		stats.SegmentsCreated += len(segmented)
		nextMax := maxRunes / 2
		if nextMax < minEmbedSegmentRunes {
			nextMax = minEmbedSegmentRunes
		}
		return embedChunksAdaptive(ctx, embedder, segmented, nextMax, overlapRunes, stats)
	}
	if IsRecoverableEmbedError(err) {
		return nil, 0, fmt.Errorf("%w: %v", errEmbedSkipFile, err)
	}
	return nil, 0, err
}

type runeWindow struct {
	start int
	end   int
}

func splitChunkForEmbedding(c *ChunkToEmbed, maxRunes, overlapRunes int) []*ChunkToEmbed {
	if c == nil {
		return nil
	}
	runes := []rune(c.Content)
	if len(runes) == 0 {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = defaultEmbedSegmentRunes
	}
	if maxRunes < minEmbedSegmentRunes {
		maxRunes = minEmbedSegmentRunes
	}
	if len(runes) <= maxRunes {
		return []*ChunkToEmbed{cloneChunkToEmbed(c)}
	}
	if overlapRunes < 0 {
		overlapRunes = 0
	}
	if overlapRunes >= maxRunes {
		overlapRunes = maxRunes / 4
	}
	if overlapRunes < 0 {
		overlapRunes = 0
	}

	windows := make([]runeWindow, 0, (len(runes)/maxRunes)+1)
	start := 0
	for start < len(runes) {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			cut := end
			lb := start + (maxRunes * 2 / 3)
			if lb > end {
				lb = start
			}
			for i := end - 1; i >= lb; i-- {
				if runes[i] == '\n' {
					cut = i + 1
					break
				}
			}
			if cut <= start {
				cut = end
			}
			end = cut
		}
		windows = append(windows, runeWindow{start: start, end: end})
		if end >= len(runes) {
			break
		}
		next := end - overlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	if len(windows) <= 1 {
		return []*ChunkToEmbed{cloneChunkToEmbed(c)}
	}

	totalLines := c.EndLine - c.StartLine + 1
	if totalLines < 1 {
		totalLines = 1
	}
	totalRunes := len(runes)
	out := make([]*ChunkToEmbed, 0, len(windows))
	for i, w := range windows {
		seg := cloneChunkToEmbed(c)
		seg.Content = string(runes[w.start:w.end])
		segStartOff := (w.start * totalLines) / totalRunes
		segEndOff := ((w.end - 1) * totalLines) / totalRunes
		seg.StartLine = c.StartLine + segStartOff
		seg.EndLine = c.StartLine + segEndOff
		if seg.EndLine < seg.StartLine {
			seg.EndLine = seg.StartLine
		}
		seg.MetadataJSON = mergeSegmentMetadata(c.MetadataJSON, i, len(windows), c.StartLine, c.EndLine, seg.StartLine, seg.EndLine)
		out = append(out, seg)
	}
	return out
}

func cloneChunkToEmbed(c *ChunkToEmbed) *ChunkToEmbed {
	if c == nil {
		return nil
	}
	cp := *c
	if len(c.MetadataJSON) > 0 {
		cp.MetadataJSON = append([]byte(nil), c.MetadataJSON...)
	}
	return &cp
}

func mergeSegmentMetadata(raw []byte, idx, total, parentStart, parentEnd, segStart, segEnd int) []byte {
	meta := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	meta["embedding_segment_index"] = idx
	meta["embedding_segment_count"] = total
	meta["embedding_parent_start_line"] = parentStart
	meta["embedding_parent_end_line"] = parentEnd
	meta["embedding_segment_start_line"] = segStart
	meta["embedding_segment_end_line"] = segEnd
	b, err := json.Marshal(meta)
	if err != nil {
		return append([]byte(nil), raw...)
	}
	return b
}

func maxSegmentRunes(cfg ChunkConfig) int {
	runes := cfg.MaxTokens * cfg.CharsPerToken
	if runes <= 0 {
		runes = defaultEmbedSegmentRunes
	}
	if runes < minEmbedSegmentRunes {
		runes = minEmbedSegmentRunes
	}
	return runes
}

func overlapRunes(cfg ChunkConfig, maxRunes int) int {
	if maxRunes <= 0 {
		maxRunes = defaultEmbedSegmentRunes
	}
	ov := maxRunes / 10
	if ov <= 0 {
		ov = defaultEmbedOverlapRunes
	}
	if ov >= maxRunes {
		ov = maxRunes / 4
	}
	if ov < 0 {
		ov = 0
	}
	return ov
}

func embedSkipReason(err error) string {
	if err == nil {
		return "none"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case IsEmbeddingProviderLimitError(err):
		return "embedding_provider_limit"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "embedding_timeout"
	case strings.Contains(msg, "status 429"):
		return "embedding_rate_limit"
	case strings.Contains(msg, "rate limit"):
		return "embedding_rate_limit"
	case strings.Contains(msg, "status 500"), strings.Contains(msg, "status 502"),
		strings.Contains(msg, "status 503"), strings.Contains(msg, "status 504"):
		return "embedding_upstream_http"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "broken pipe"):
		return "embedding_network"
	default:
		// Recoverable per IsRecoverableEmbedError but not one of the cases above (see substring list in recoverable.go).
		return "recoverable_api_error"
	}
}
