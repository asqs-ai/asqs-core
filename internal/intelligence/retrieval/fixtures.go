package retrieval

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// pathPatternSearcher is implemented by stores that can rank path-matched chunks by vector
// distance. Optional so a ChunkReader without it falls back to the previous listing behaviour.
type pathPatternSearcher interface {
	SearchByPathPattern(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions, pathSubstrings []string) ([]embeddings.SearchResult, error)
}

// relevantChunksByPathPattern returns path-matched chunks ranked by relevance to the target.
//
// Falls back to the alphabetical listing when the store has no ranked search or the target has no
// embedding, so behaviour degrades rather than breaking.
func relevantChunksByPathPattern(ctx context.Context, chunks ChunkReader, target *embeddings.Chunk, repoID, lang string, substrings []string, limit int) []*embeddings.Chunk {
	if limit <= 0 {
		return nil
	}
	ps, ok := chunks.(pathPatternSearcher)
	if !ok || target == nil || len(target.Embedding) == 0 {
		return listChunksByPathPattern(ctx, chunks, repoID, lang, substrings, limit)
	}
	rows, err := ps.SearchByPathPattern(ctx, target.Embedding, embeddings.SearchOptions{
		RepoID: repoID,
		Lang:   lang,
		Limit:  limit,
	}, substrings)
	if err != nil || len(rows) == 0 {
		return listChunksByPathPattern(ctx, chunks, repoID, lang, substrings, limit)
	}
	out := make([]*embeddings.Chunk, 0, len(rows))
	for i := range rows {
		cp := rows[i].Chunk
		out = append(out, &cp)
	}
	return out
}

// configChunksByPathProximity returns config chunks ordered by directory distance from the target
// file, nearest first.
//
// Vector distance is the wrong signal here and ranking by it would be a different kind of arbitrary:
// `application-test.yml` shares almost no vocabulary with a service method body, so cosine between
// them is noise. Path proximity is deterministic, explainable, and matches how a developer actually
// looks for the configuration governing a class — start in its directory and walk upward.
func configChunksByPathProximity(ctx context.Context, chunks ChunkReader, target *embeddings.Chunk, repoID, lang string, substrings []string, limit int) []*embeddings.Chunk {
	if limit <= 0 {
		return nil
	}
	// Over-fetch so there is something to rank; the alphabetical listing is only a candidate source
	// here, not the final order.
	candidates := listChunksByPathPattern(ctx, chunks, repoID, lang, substrings, limit*10)
	if len(candidates) == 0 {
		return nil
	}
	targetDir := ""
	if target != nil {
		targetDir = filepath.ToSlash(filepath.Dir(strings.TrimSpace(target.File)))
	}
	if targetDir == "" || targetDir == "." {
		if len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}

	type scored struct {
		c    *embeddings.Chunk
		dist int
	}
	rows := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if c == nil {
			continue
		}
		rows = append(rows, scored{c: c, dist: pathProximity(targetDir, filepath.ToSlash(filepath.Dir(c.File)))})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].dist != rows[j].dist {
			return rows[i].dist < rows[j].dist
		}
		return rows[i].c.File < rows[j].c.File
	})
	out := make([]*embeddings.Chunk, 0, limit)
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		out = append(out, r.c)
	}
	return out
}

// pathProximity returns the number of directory steps between two directories: the segments each
// must drop to reach their common ancestor, summed. Identical directories score 0.
func pathProximity(a, b string) int {
	as := splitDirSegments(a)
	bs := splitDirSegments(b)
	common := 0
	for common < len(as) && common < len(bs) && as[common] == bs[common] {
		common++
	}
	return (len(as) - common) + (len(bs) - common)
}

func splitDirSegments(p string) []string {
	p = strings.Trim(filepath.ToSlash(strings.TrimSpace(p)), "/")
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}
