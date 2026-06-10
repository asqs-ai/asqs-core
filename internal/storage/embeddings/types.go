package embeddings

// Chunk is a unit of source code (or related text) stored with its embedding for RAG.
type Chunk struct {
	ID        string    // UUID
	Content   string    // raw text (e.g. code snippet, docstring)
	Embedding []float32 // vector; length must match store dimension
	// Provenance and filters for symbol-aware RAG
	SymbolID string // optional; links to metadata.symbols.id
	// ParentSymbolID is the CONTAINS parent symbol id (metadata.symbols.id) when known; optional denormalized column.
	ParentSymbolID string
	File           string // source file path
	Lang           string // e.g. "java", "csharp"
	ChunkType      string // e.g. "definition", "body", "test", "call_site", "comment"
	StartLine      int    // 1-based
	EndLine        int    // 1-based
	RepoID         string // optional; for multi-repo
	// MetadataJSON is optional JSON (e.g. symbol_kind, fq_name, chunk_index, parent_fq) for filters without parsing content.
	MetadataJSON []byte
}

// SearchResult is a chunk returned from vector or hybrid search, with optional distance/score.
type SearchResult struct {
	Chunk
	Distance float64 // L2 distance (pgvector <->); lower = more similar
}

// DefaultEmbeddingDim is the default vector dimension (e.g. OpenAI text-embedding-3-small).
const DefaultEmbeddingDim = 1536

// EmbeddingProvider describes which provider/model produced the vectors currently stored.
type EmbeddingProvider struct {
	Provider       string // e.g. "openai"
	EmbeddingModel string // e.g. "text-embedding-3-small"
	Dimension      int
	UpdatedAt      string // ISO8601 or empty
}
