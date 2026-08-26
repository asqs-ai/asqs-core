// Package llembed implements multi-provider embeddings via go-embeddings and shared input normalization.
package llembed

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// L2Normalize returns v scaled to unit length. A zero vector is returned unchanged (there is no
// meaningful direction to preserve). The input is not modified.
//
// Why this matters: the HNSW index is built with vector_l2_ops and Search orders by `<->` (L2),
// but *every* consumer scores with cosine — MMR relevance and redundancy (mmr.go), the abstention
// floor (MinSimilarityCosine), and the project-intel rerank. L2 and cosine induce the same ordering
// only when all vectors have equal norm.
//
// For OpenAI text-embedding-3-* they do, so the mismatch was latent there. For local models served
// through Ollama (nomic-embed-text and friends) norms vary meaningfully with input length, so
// candidate *generation* was ordered by L2 while candidate *ranking*, diversification and the
// abstention decision used cosine: a long chunk with a large-norm embedding is L2-far even when its
// cosine is high (so HNSW never surfaced it), and a short small-norm chunk is L2-near even when its
// cosine is mediocre (so it displaced something better). Silent in both directions.
//
// With unit-norm vectors, L2² = 2 − 2·cos, so the existing L2 index and `<->` ordering become
// exactly cosine ordering — no index change and no query change — and SearchResult.Distance becomes
// a comparable quantity for the first time.
func L2Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1.0 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// IsUnitNorm reports whether v is already unit length within tol. Used by tests and by the
// migration to decide whether a corpus needs normalizing.
func IsUnitNorm(v []float32, tol float64) bool {
	if len(v) == 0 {
		return false
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Abs(math.Sqrt(sum)-1.0) <= tol
}

// NormalizingEmbedder wraps an Embedder and L2-normalizes every vector it returns, so the guarantee
// holds for every provider rather than only the ones that happen to emit unit vectors.
type NormalizingEmbedder struct {
	Inner model.Embedder
}

// NewNormalizingEmbedder wraps inner unless it is nil or already a NormalizingEmbedder.
func NewNormalizingEmbedder(inner model.Embedder) model.Embedder {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*NormalizingEmbedder); ok {
		return inner
	}
	return &NormalizingEmbedder{Inner: inner}
}

// Embed implements model.Embedder.
func (e *NormalizingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := e.Inner.Embed(ctx, texts)
	for i := range vecs {
		vecs[i] = L2Normalize(vecs[i])
	}
	return vecs, err
}

// MaxEmbeddingInputRunes caps each input to reduce token-limit failures (aligned with prior OpenAI embedder).
const MaxEmbeddingInputRunes = 30000

// NormalizeTexts trims inputs, replaces empty strings with a minimal token, and truncates at the
// legacy flat cap. Prefer NormalizeTextsForModel, which uses the configured model's real limit.
func NormalizeTexts(texts []string) []string {
	out, _ := NormalizeTextsWithLimit(texts, MaxEmbeddingInputRunes)
	return out
}

// NormalizeTextsForModel is NormalizeTexts with the cap derived from the provider and model.
// Returns the number of inputs that had to be truncated.
func NormalizeTextsForModel(texts []string, provider, model string) ([]string, int) {
	return NormalizeTextsWithLimit(texts, MaxInputRunes(provider, model))
}

// NormalizeTextsWithLimit trims, substitutes empties, and truncates each input to maxRunes.
// The second return value is the number of inputs that were truncated — a signal that previously
// did not exist at all.
func NormalizeTextsWithLimit(texts []string, maxRunes int) ([]string, int) {
	if maxRunes <= 0 {
		maxRunes = MaxEmbeddingInputRunes
	}
	inputs := make([]string, len(texts))
	truncated := 0
	for i, t := range texts {
		s := strings.TrimSpace(t)
		if s == "" {
			inputs[i] = " "
			continue
		}
		if utf8.RuneCountInString(s) > maxRunes {
			runes := []rune(s)
			s = string(runes[:maxRunes])
			truncated++
		}
		inputs[i] = s
	}
	return inputs, truncated
}

// TruncationCount is the process-wide number of embedding inputs that had to be truncated.
//
// This counter is the real deliverable of the input-limit work. The chunker targets MaxTokens 800
// (~3 200 runes), so primary chunks should be far under any model's limit — which means a non-zero
// count says the chunker is emitting oversized chunks (Angular templates, or a symbol whose span
// extraction went wrong). That is a separate bug worth knowing about, and it was previously
// invisible.
var truncationCount atomic.Int64

// NoteTruncated records n truncated embedding inputs.
func NoteTruncated(n int) {
	if n > 0 {
		truncationCount.Add(int64(n))
	}
}

// TruncationCount returns the number of embedding inputs truncated since process start.
func TruncationCount() int64 { return truncationCount.Load() }
