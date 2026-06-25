package llembed

import "strings"

// knownEmbeddingDimensions maps well-known embedding model names to their output
// dimension. Used ONLY to warn on a store-vs-model dimension mismatch (see the
// llm.NewEmbedder call sites); it never resizes the store, which would truncate
// existing chunks. A model that is not present here returns 0 = unknown, and the
// caller skips the warning.
var knownEmbeddingDimensions = map[string]int{
	"nomic-embed-text":       768,
	"mxbai-embed-large":      1024,
	"all-minilm":             384,
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
}

// EmbeddingDimensionForModel returns the known output dimension for an embedding
// model name, or 0 if unknown. The lookup is case-insensitive and ignores an
// Ollama ":tag" suffix (e.g. "nomic-embed-text:latest" → 768).
func EmbeddingDimensionForModel(modelName string) int {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return 0
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return knownEmbeddingDimensions[name]
}
