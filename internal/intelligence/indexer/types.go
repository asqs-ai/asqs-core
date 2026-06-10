package indexer

import (
	"context"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// IndexRun represents a single indexing run (version for scheduling and incremental updates).
type IndexRun struct {
	RunID     string // unique id (e.g. UUID or commit SHA)
	RepoID    string
	CommitSHA string // repo commit at index time
	Started   int64  // unix ms
	Finished  int64
}

// FileVersion is the current state of a file in the repo (path + content hash for change detection).
type FileVersion struct {
	Path   string
	SHA    string // git blob or content hash
	Lang   string // java, csharp
	Module string
	IsTest bool
}

// ChangeSet is the result of change detection: which files to add, update, or remove.
type ChangeSet struct {
	Added   []FileVersion
	Changed []FileVersion
	Removed []string // file paths
}

// ParsedSymbol is one symbol from the language indexer (AST/symbol table).
type ParsedSymbol struct {
	Kind      string
	FQName    string
	StartLine int
	EndLine   int
	// StartColumn/EndColumn are optional 1-based columns on StartLine/EndLine when the parser provides them.
	StartColumn   *int
	EndColumn     *int
	SignatureJSON []byte
}

// ParsedEdge is a dependency edge from the language indexer.
type ParsedEdge struct {
	CallerFQName string
	CalleeFQName string
	EdgeType     string
	// Optional JSON metadata from the helper (e.g. PACKAGE_EXPORT condition chains); not persisted on metadata.Edge today.
	SignatureJSON []byte
}

// ParsedFile is the output of the language-specific indexer for one file (AST-derived symbol table + graph).
type ParsedFile struct {
	Path    string
	Lang    string
	Module  string
	IsTest  bool
	Symbols []ParsedSymbol
	Edges   []ParsedEdge
	Source  string // full file content for chunking
}

// ChunkPlan describes one chunk to be created (symbol-boundary, sanitized, within token budget).
type ChunkPlan struct {
	Content   string
	SymbolID  string // set after symbol is stored
	File      string
	Lang      string
	ChunkType string
	StartLine int
	EndLine   int
	RepoID    string
	// SymbolFQ is the primary symbol for symbol_id resolution (first symbol when merged).
	SymbolFQ      string
	SymbolKind    string
	ChunkIndex    int // 0 = single or first slice; >0 for large-symbol continuations (Phase D).
	ParentFQ      string
	SecondaryRole string // e.g. route_manifest, angular_template_file (Phase C); empty for normal chunks.
	MetadataJSON  []byte // stored as chunk_metadata JSONB when non-nil (Phases A–D).
}

// ChunkToEmbed is a chunk ready for embedding and storage (no vector yet).
type ChunkToEmbed struct {
	Content      string
	SymbolID     string
	File         string
	Lang         string
	ChunkType    string
	StartLine    int
	EndLine      int
	RepoID       string
	MetadataJSON []byte // optional; persisted as chunks.chunk_metadata
	// ParentSymbolID is resolved in Run from ParentFQ → metadata symbol id; stored as chunks.parent_symbol_id.
	ParentSymbolID string
}

// ToChunk converts a ChunkToEmbed and embedding vector into an embeddings.Chunk for storage.
func (c *ChunkToEmbed) ToChunk(embedding []float32) *embeddings.Chunk {
	ch := &embeddings.Chunk{
		Content:        c.Content,
		Embedding:      embedding,
		SymbolID:       c.SymbolID,
		File:           c.File,
		Lang:           c.Lang,
		ChunkType:      c.ChunkType,
		StartLine:      c.StartLine,
		EndLine:        c.EndLine,
		RepoID:         c.RepoID,
		ParentSymbolID: c.ParentSymbolID,
	}
	if len(c.MetadataJSON) > 0 {
		ch.MetadataJSON = append([]byte(nil), c.MetadataJSON...)
	}
	return ch
}

// MetadataWriter is the subset of metadata.Store needed for indexing (write symbols, edges, files; delete for incremental; index runs).
type MetadataWriter interface {
	InsertSymbol(ctx context.Context, sym *metadata.Symbol) (string, error)
	InsertEdge(ctx context.Context, e *metadata.Edge) error
	UpsertFile(ctx context.Context, f *metadata.File) error
	DeleteSymbolsByFile(ctx context.Context, file string) (int64, error)
	DeleteFile(ctx context.Context, file string) error
	GetFile(ctx context.Context, file string) (*metadata.File, error)
	ListFiles(ctx context.Context, lang string, isTest *bool) ([]*metadata.File, error)
	ListSymbolsByFQName(ctx context.Context, fqName string) ([]*metadata.Symbol, error)
	// MaterializeTestsSourceEdges rebuilds TESTS_SOURCE edges after indexing (test→SUT heuristics). Mocks may return (0, nil).
	MaterializeTestsSourceEdges(ctx context.Context) (inserted int, err error)
	InsertIndexRun(ctx context.Context, runID, repoID, commitSHA string, startedAt int64, currentIteration int, extras *metadata.IndexRunStartExtras) error
	UpdateIndexRunFinished(ctx context.Context, runID string, finishedAt int64) error
	// CountSymbols returns the total number of symbols currently stored. The indexer calls this
	// after writes finish so RunResult.SymbolsTotal reports the post-run count (A.7). Mock
	// implementations may return (0, nil); the indexer treats counting failures as best-effort.
	CountSymbols(ctx context.Context) (int64, error)
	// CountEdges returns the total number of edges currently stored. Best-effort; see CountSymbols.
	CountEdges(ctx context.Context) (int64, error)
	// CountIndexRuns returns the number of index_runs rows for the given repo (including the
	// current run, which has already been inserted by indexer.Run before this call). The
	// indexer uses the count to detect "first run for this repo": when count <= 1, no prior
	// index run exists and the indexer forces a full reindex even when the global `files`
	// table has matching SHAs from other projects (false cache hit on a multi-tenant DB).
	// Implementations may surface a store error; the indexer treats errors as "first-run
	// signal unknown" and forces reindex to be safe.
	CountIndexRuns(ctx context.Context, repoID string) (int64, error)
}

// EmbeddingsWriter is the subset of embeddings.Store needed for indexing.
type EmbeddingsWriter interface {
	InsertChunks(ctx context.Context, chunks []*embeddings.Chunk) ([]string, error)
	DeleteByFile(ctx context.Context, file string) (int64, error)
	DeleteByRepo(ctx context.Context, repoID string) (int64, error)
	SetEmbeddingProvider(ctx context.Context, provider, embeddingModel string, dimension int) error
	// CountChunksByRepo returns the total number of chunks currently stored for the given repo
	// (empty repoID counts all repos). The indexer calls this after writes complete to populate
	// RunResult.ChunksTotal (A.7). Mock implementations may return (0, nil); the indexer treats
	// counting failures as best-effort and continues with a zero total.
	CountChunksByRepo(ctx context.Context, repoID string) (int64, error)
}

// LangIndexer runs the language-specific parser (e.g. Java C# helper) and returns parsed file data.
type LangIndexer func(ctx context.Context, path string, lang string, source []byte) (*ParsedFile, error)

// Embedder produces embedding vectors for a batch of texts (e.g. via LLM API).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
