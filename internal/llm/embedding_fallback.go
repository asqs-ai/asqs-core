package llm

import (
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
)

// DefaultEmbeddingFallbackModel is the Ollama embedding model used when the
// configured provider cannot produce embeddings and llm.embedding_fallback is
// enabled with "auto"/"default". 768-dim; requires `ollama pull nomic-embed-text`.
const DefaultEmbeddingFallbackModel = "nomic-embed-text"

// embeddingFallbackModel returns the embedding model to fall back to when the
// configured provider has no embeddings, or "" when the fallback is disabled.
// llm.embedding_fallback: "" = disabled (default); "auto"/"default"/"on"/"true"/
// "yes" = DefaultEmbeddingFallbackModel; any other value = that explicit Ollama
// embedding model name (e.g. "mxbai-embed-large").
func embeddingFallbackModel(cfg *config.Config) string {
	v := strings.TrimSpace(cfg.LLM.EmbeddingFallback)
	switch strings.ToLower(v) {
	case "":
		return ""
	case "auto", "default", "on", "true", "yes":
		return DefaultEmbeddingFallbackModel
	default:
		return v
	}
}

// effectiveEmbeddingModelName returns the embedding model that will actually be
// used: the explicitly configured llm.embedding_model when set, otherwise the
// resolved fallback model (which is "" when the fallback is disabled). Used to
// warn about a store/model dimension mismatch before indexing begins.
func effectiveEmbeddingModelName(cfg *config.Config) string {
	if m := strings.TrimSpace(cfg.LLM.EmbeddingModel); m != "" {
		return m
	}
	return embeddingFallbackModel(cfg)
}

// dimensionMismatchWarningForModel returns a non-empty, actionable warning when
// the known dimension of model differs from storeDimension; otherwise "". It
// returns "" when the model is unknown (dimension 0) or storeDimension is 0, so
// callers can print it unconditionally.
func dimensionMismatchWarningForModel(model string, storeDimension int) string {
	want := llembed.EmbeddingDimensionForModel(model)
	if want == 0 || storeDimension <= 0 || want == storeDimension {
		return ""
	}
	return fmt.Sprintf("embedding model %q is %d-dim but database.embeddings_dimension is %d; "+
		"chunk inserts will fail — set database.embeddings_dimension: %d (and reindex)",
		strings.TrimSpace(model), want, storeDimension, want)
}

// DimensionMismatchWarning returns an actionable warning when the effective
// embedding model's known dimension differs from the configured store dimension,
// otherwise "". Call this at embedder-construction sites (passing the store's
// dimension) so a nomic fallback (768) against a 1536 store is reported up front
// rather than as a cryptic per-chunk insert error mid-index.
func DimensionMismatchWarning(cfg *config.Config, storeDimension int) string {
	return dimensionMismatchWarningForModel(effectiveEmbeddingModelName(cfg), storeDimension)
}
