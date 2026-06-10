package retrieval

import (
	"fmt"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// Default abstention thresholds when retrieval.abstention_disabled is false and YAML/env leave the
// corresponding field at zero (resolved in orchestrator.BuildPlanOptions).
const (
	// DefaultAbstentionMinSimilarTests is 0 so greenfield repos (no indexed tests yet) are not blocked.
	// Set retrieval.min_similar_tests_for_generation ≥ 1 to require anchor examples.
	DefaultAbstentionMinSimilarTests     = 0
	DefaultAbstentionMinSimilarityCosine = 0.5
)

// AssessSimilarReferenceSufficiency decides whether retrieved similar-reference chunks (the
// RetrievalContext.SimilarTests slice — profile-selected types such as test, route, e2e_pattern)
// are strong enough anchors for test generation. When both minSimilarCount and minSimilarityCosine
// are <= 0, the policy is disabled and the function always returns ok=true.
//
// minSimilarCount (>0): require at least this many non-nil entries in similar.
//
// minSimilarityCosine (>0): when the target embedding is non-empty and at least one similar chunk
// exists, require that some similar chunk with a non-empty embedding achieves at least this cosine
// similarity to the target. When similarCount is 0, the cosine criterion is not applied so
// greenfield projects (no retrieved test anchors) can still proceed. When the target has no
// embedding, the cosine criterion is not applied. Typical values are on [0, 1]; values > 1 clamp to 1; ≤ 0 disables this criterion.
func AssessSimilarReferenceSufficiency(targetEmb []float32, similar []*embeddings.Chunk, minSimilarCount int, minSimilarityCosine float64) (ok bool, reason string, similarCount int, maxCosine float64, cosineCriterionApplied bool) {
	minCos := clampCosineThreshold(minSimilarityCosine)
	similarCount = countNonNilSimilarChunks(similar)
	maxCosine = maxSimilarityToTarget(targetEmb, similar)
	cosineCriterionApplied = len(targetEmb) > 0 && minCos > 0 && similarCount > 0

	if minSimilarCount <= 0 && minCos <= 0 {
		return true, "", similarCount, maxCosine, cosineCriterionApplied
	}
	if minSimilarCount > 0 && similarCount < minSimilarCount {
		return false, fmt.Sprintf("similar_reference_count=%d < min_similar_tests_for_generation=%d", similarCount, minSimilarCount), similarCount, maxCosine, cosineCriterionApplied
	}
	const cosEps = 1e-9
	if minCos > 0 && len(targetEmb) > 0 && similarCount > 0 && maxCosine+cosEps < minCos {
		return false, fmt.Sprintf("max_cosine_similarity=%.4f < min_similarity_cosine=%.4f", maxCosine, minCos), similarCount, maxCosine, cosineCriterionApplied
	}
	return true, "", similarCount, maxCosine, cosineCriterionApplied
}

func clampCosineThreshold(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func countNonNilSimilarChunks(similar []*embeddings.Chunk) int {
	n := 0
	for _, ch := range similar {
		if ch != nil {
			n++
		}
	}
	return n
}

// TargetMethodEmbedding returns the target symbol chunk embedding for sufficiency checks, or nil.
func TargetMethodEmbedding(rc *RetrievalContext) []float32 {
	if rc == nil || rc.TargetMethod == nil || rc.TargetMethod.Chunk == nil {
		return nil
	}
	return rc.TargetMethod.Chunk.Embedding
}

func maxSimilarityToTarget(targetEmb []float32, similar []*embeddings.Chunk) float64 {
	if len(targetEmb) == 0 {
		return 0
	}
	var max float64
	for _, ch := range similar {
		if ch == nil || len(ch.Embedding) == 0 {
			continue
		}
		if c := cosineSimilarity(targetEmb, ch.Embedding); c > max {
			max = c
		}
	}
	return max
}
