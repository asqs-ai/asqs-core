package retrieval

import (
	"math"
	"sort"
	"strconv"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// Default values for Maximal Marginal Relevance (MMR) over embedding space, following
// Carbonell & Goldstein, "The Use of MMR, Diversity-Based Reranking for Reordering Documents
// and Producing Summaries" (SIGIR 1998). Relevance and redundancy use cosine similarity,
// the usual choice for dense retrieval in RAG pipelines.
const (
	defaultSimilarMMRLambda  = 0.5
	similarMMRPoolMultiplier = 4
	similarMMRPoolMinExtra   = 8
	similarMMRPoolMax        = 120
)

// normalizeSimilarMMRLambda returns λ in (0,1] for MMR. Zero, negative, NaN, or values > 1 fall back to defaultSimilarMMRLambda.
func normalizeSimilarMMRLambda(v float64) float64 {
	if v <= 0 || v > 1 || math.IsNaN(v) {
		return defaultSimilarMMRLambda
	}
	return v
}

// similarReferenceSearchPoolSize is how many nearest neighbors we fetch per chunk_type before MMR.
// Must exceed the final k so MMR can trade relevance for diversity.
func similarReferenceSearchPoolSize(limit int) int {
	if limit <= 0 {
		limit = 5
	}
	pool := limit * similarMMRPoolMultiplier
	if pool < limit+similarMMRPoolMinExtra {
		pool = limit + similarMMRPoolMinExtra
	}
	if pool > similarMMRPoolMax {
		pool = similarMMRPoolMax
	}
	return pool
}

// cosineSimilarity returns the cosine of the angle between a and b, or 0 if lengths differ or either norm is zero.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		na += af * af
		nb += bf * bf
	}
	if na == 0 || nb == 0 {
		return 0
	}
	cos := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if cos > 1 {
		return 1
	}
	if cos < -1 {
		return -1
	}
	return cos
}

// chunkStableKey identifies a chunk for deduplication (prefer id, else file+start+type).
func chunkStableKey(ch *embeddings.Chunk) string {
	if ch == nil {
		return ""
	}
	if ch.ID != "" {
		return ch.ID
	}
	return ch.File + "\x00" + strconv.Itoa(ch.StartLine) + "\x00" + ch.ChunkType
}

// mmrScoredChunk holds a candidate and its relevance Sim(q, d) for MMR.
type mmrScoredChunk struct {
	chunk     embeddings.Chunk
	relevance float64
}

// maximalMarginalRelevance selects up to k chunks. The first pick maximizes relevance; each further pick maximizes
// λ·Sim(q,d) − (1−λ)·max_{s∈S} Sim(d,s) (standard MMR). Ties break on stable chunk key (lexicographic).
func maximalMarginalRelevance(query []float32, candidates []mmrScoredChunk, k int, lambda float64) []*embeddings.Chunk {
	if k <= 0 || len(candidates) == 0 {
		return nil
	}
	if lambda < 0 || lambda > 1 || math.IsNaN(lambda) {
		lambda = defaultSimilarMMRLambda
	}

	remaining := make([]int, len(candidates))
	for i := range candidates {
		remaining[i] = i
	}
	var picked []int

	for len(picked) < k && len(remaining) > 0 {
		bestRi := -1
		bestScore := math.Inf(-1)
		bestTie := ""
		bestRedundancy := math.Inf(1) // maxSim to S; lower is more diverse (tie-break when MMR scores match)

		for pos, idx := range remaining {
			rel := candidates[idx].relevance
			key := chunkStableKey(&candidates[idx].chunk)

			var score float64
			maxSim := 0.0
			if len(picked) == 0 {
				score = rel
			} else {
				for _, pj := range picked {
					s := cosineSimilarity(candidates[idx].chunk.Embedding, candidates[pj].chunk.Embedding)
					if s > maxSim {
						maxSim = s
					}
				}
				score = lambda*rel - (1.0-lambda)*maxSim
			}

			tieKey := key
			if tieKey == "" {
				tieKey = "\xff"
			}
			better := false
			switch {
			case score > bestScore+1e-12:
				better = true
			case math.Abs(score-bestScore) <= 1e-12:
				if len(picked) == 0 {
					better = bestRi < 0 || tieKey < bestTie
				} else {
					// Same MMR score: prefer lower redundancy (standard when breaking ties among equi-scoring diversifications).
					better = maxSim < bestRedundancy-1e-12 || (math.Abs(maxSim-bestRedundancy) <= 1e-12 && (bestRi < 0 || tieKey < bestTie))
				}
			}
			if better {
				bestScore = score
				bestRi = pos
				bestTie = tieKey
				if len(picked) > 0 {
					bestRedundancy = maxSim
				}
			}
		}

		if bestRi < 0 {
			break
		}
		selIdx := remaining[bestRi]
		picked = append(picked, selIdx)
		remaining = append(remaining[:bestRi], remaining[bestRi+1:]...)
	}

	out := make([]*embeddings.Chunk, 0, len(picked))
	for _, idx := range picked {
		cp := candidates[idx].chunk
		c := cp
		out = append(out, &c)
	}
	return out
}

// sortMMRPool sorts candidates for deterministic behavior before MMR (descending relevance, then key).
func sortMMRPool(pool []mmrScoredChunk) {
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].relevance != pool[j].relevance {
			return pool[i].relevance > pool[j].relevance
		}
		ki := chunkStableKey(&pool[i].chunk)
		kj := chunkStableKey(&pool[j].chunk)
		return ki < kj
	})
}
