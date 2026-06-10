package embeddings

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

//go:embed schema.sql
var schemaFS embed.FS

var vectorColumnDimRE = regexp.MustCompile(`(?i)\bvector\s*\(\s*(\d+)\s*\)`)

const defaultDimension = 1536

// Store provides chunk + embedding storage and vector search for symbol-aware RAG.
type Store struct {
	pool *pgxpool.Pool
	dim  int
}

// Config configures the embeddings store.
type Config struct {
	ConnString string // Postgres connection string (same DB as metadata is fine)
	Dimension  int    // embedding dimension; 0 = DefaultEmbeddingDim (1536)
}

// Open creates a connection pool and registers pgvector types. Call InitSchema to create tables.
// The vector extension is created automatically if missing, so the DB is ready before pgvector type registration.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.ConnString == "" {
		return nil, fmt.Errorf("embeddings: ConnString required")
	}
	dim := cfg.Dimension
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	// Ensure the vector extension exists before we register pgvector types (RegisterTypes requires the type in the DB).
	conn, err := pgx.Connect(ctx, cfg.ConnString)
	if err != nil {
		return nil, fmt.Errorf("embeddings: connect: %w", err)
	}
	_, _ = conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	_ = conn.Close(ctx)

	config, err := pgxpool.ParseConfig(cfg.ConnString)
	if err != nil {
		return nil, fmt.Errorf("embeddings: parse config: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("embeddings: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("embeddings: ping: %w", err)
	}
	return &Store{pool: pool, dim: dim}, nil
}

// Close closes the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// InitSchema creates the vector extension and chunks table (and indexes) if they do not exist.
func (s *Store) InitSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("embeddings: create extension: %w", err)
	}
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("embeddings: read schema: %w", err)
	}
	// Strip line comments before splitting on ';' — otherwise a semicolon inside "-- ...; ..." breaks statements
	// (e.g. "instance; when" produced: syntax error at or near "when").
	sql := stripSQLLineComments(string(b))
	// DDL defaults are OpenAI-sized (1536). Rewrite so fresh installs match this store's dimension.
	sql = strings.ReplaceAll(sql, "vector(1536)", fmt.Sprintf("vector(%d)", s.dim))
	sql = strings.ReplaceAll(sql, "DEFAULT 1536", fmt.Sprintf("DEFAULT %d", s.dim))
	sql = strings.ReplaceAll(sql, "VALUES (1, '', '', 1536)", fmt.Sprintf("VALUES (1, '', '', %d)", s.dim))
	for _, stmt := range splitSQL(sql) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("embeddings: exec schema: %w", err)
		}
	}
	if err := s.alignChunksEmbeddingColumn(ctx); err != nil {
		return err
	}
	return nil
}

func parseVectorColumnDim(pgFormatType string) int {
	if pgFormatType == "" {
		return 0
	}
	m := vectorColumnDimRE.FindStringSubmatch(pgFormatType)
	if len(m) < 2 {
		return 0
	}
	d, err := strconv.Atoi(m[1])
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// alignChunksEmbeddingColumn fixes chunks.embedding when an older schema used vector(1536) (or another size)
// but this store expects s.dim. Existing vectors are incompatible after an embedding model/dimension change,
// so rows are truncated before ALTER TYPE.
func (s *Store) alignChunksEmbeddingColumn(ctx context.Context) error {
	var colType string
	err := s.pool.QueryRow(ctx, `
		SELECT pg_catalog.format_type(a.atttypid, a.atttypmod)
		FROM pg_catalog.pg_attribute a
		INNER JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname = 'chunks'
		  AND a.attname = 'embedding'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	`).Scan(&colType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("embeddings: read chunks.embedding type: %w", err)
	}
	cur := parseVectorColumnDim(colType)
	if cur <= 0 || cur == s.dim {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("embeddings: migrate embedding dimension: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DROP INDEX IF EXISTS idx_chunks_embedding_hnsw`); err != nil {
		return fmt.Errorf("embeddings: migrate dimension drop index: %w", err)
	}
	if _, err := tx.Exec(ctx, `TRUNCATE TABLE chunks`); err != nil {
		return fmt.Errorf("embeddings: migrate dimension truncate chunks: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE chunks ALTER COLUMN embedding TYPE vector(%d)`, s.dim)); err != nil {
		return fmt.Errorf("embeddings: migrate dimension alter column: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_l2_ops)`); err != nil {
		return fmt.Errorf("embeddings: migrate dimension recreate hnsw: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE embedding_provider SET dimension = $1, updated_at = NOW() WHERE id = 1`, s.dim); err != nil {
		return fmt.Errorf("embeddings: migrate dimension update embedding_provider: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("embeddings: migrate dimension commit: %w", err)
	}
	return nil
}

func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, part)
		}
	}
	return out
}

// stripSQLLineComments removes PostgreSQL line comments (-- …) from each line before splitSQL runs.
// DDL here does not use '--' inside string literals; this avoids semicolons inside comments splitting statements.
func stripSQLLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(strings.TrimRight(line, " \t\r"))
		b.WriteByte('\n')
	}
	return b.String()
}

// Dimension returns the embedding dimension expected by this store.
func (s *Store) Dimension() int { return s.dim }

// InsertChunk inserts a single chunk and returns its ID. Embedding length must match Dimension().
func (s *Store) InsertChunk(ctx context.Context, c *Chunk) (id string, err error) {
	if len(c.Embedding) != s.dim {
		return "", fmt.Errorf("embeddings: embedding length %d != store dimension %d", len(c.Embedding), s.dim)
	}
	vec := pgvector.NewVector(c.Embedding)
	symbolID := nullUUID(c.SymbolID)
	chunkType := c.ChunkType
	if chunkType == "" {
		chunkType = "definition"
	}
	metaArg := any(nil)
	if len(c.MetadataJSON) > 0 {
		metaArg = c.MetadataJSON
	}
	parentID := nullUUID(c.ParentSymbolID)
	row := s.pool.QueryRow(ctx, `
		INSERT INTO chunks (content, embedding, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id, chunk_metadata, parent_symbol_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		sanitizeTextForPostgres(c.Content), vec, symbolID, sanitizeTextForPostgres(c.File), sanitizeTextForPostgres(c.Lang),
		sanitizeTextForPostgres(chunkType), c.StartLine, c.EndLine, sanitizeTextForPostgres(c.RepoID), metaArg, parentID,
	)
	err = row.Scan(&id)
	return id, err
}

// InsertChunks inserts multiple chunks in one transaction. Returns the list of assigned IDs in order.
func (s *Store) InsertChunks(ctx context.Context, chunks []*Chunk) (ids []string, err error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids = make([]string, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Embedding) != s.dim {
			return nil, fmt.Errorf("embeddings: embedding length %d != store dimension %d", len(c.Embedding), s.dim)
		}
		vec := pgvector.NewVector(c.Embedding)
		symbolID := nullUUID(c.SymbolID)
		chunkType := c.ChunkType
		if chunkType == "" {
			chunkType = "definition"
		}
		var id string
		metaArg := any(nil)
		if len(c.MetadataJSON) > 0 {
			metaArg = c.MetadataJSON
		}
		parentID := nullUUID(c.ParentSymbolID)
		err = tx.QueryRow(ctx, `
			INSERT INTO chunks (content, embedding, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id, chunk_metadata, parent_symbol_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id`,
			sanitizeTextForPostgres(c.Content), vec, symbolID, sanitizeTextForPostgres(c.File), sanitizeTextForPostgres(c.Lang),
			sanitizeTextForPostgres(chunkType), c.StartLine, c.EndLine, sanitizeTextForPostgres(c.RepoID), metaArg, parentID,
		).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit(ctx)
}

// GetByID returns the chunk with the given ID, or nil if not found.
func (s *Store) GetByID(ctx context.Context, id string) (*Chunk, error) {
	var c Chunk
	var vec pgvector.Vector
	var symbolID *string
	var meta []byte
	var parentID *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, content, embedding, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id, chunk_metadata, parent_symbol_id
		FROM chunks WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.Content, &vec, &symbolID, &c.File, &c.Lang, &c.ChunkType, &c.StartLine, &c.EndLine, &c.RepoID, &meta, &parentID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	c.Embedding = vec.Slice()
	if symbolID != nil {
		c.SymbolID = *symbolID
	}
	if len(meta) > 0 {
		c.MetadataJSON = append([]byte(nil), meta...)
	}
	if parentID != nil {
		c.ParentSymbolID = *parentID
	}
	return &c, nil
}

// SearchOptions narrows vector search (filters and limit).
// Structured filters (Module, MetadataContains) combine with dense ranking in one SQL query: WHERE … ORDER BY embedding <-> query (hybrid retrieval; see Lewis et al. RAG, Gao et al. survey).
type SearchOptions struct {
	Limit          int    // max results; default 10
	File           string // filter by exact file path (if set, FilePrefix is ignored)
	FilePrefix     string // filter by path prefix using forward slashes (substring match; empty = no filter)
	Lang           string // filter by lang
	SymbolID       string // filter by symbol_id
	ParentSymbolID string // filter by parent_symbol_id (container symbol)
	RepoID         string // filter by repo_id
	ChunkType      string // filter by chunk_type
	// Module filters chunk_metadata->>'module' (exact). Empty = no filter. Populated by indexer chunk metadata.
	Module string
	// MetadataContains is a JSON object that must be contained in chunk_metadata (PostgreSQL @>). Empty = no filter.
	MetadataContains []byte
}

// Search returns chunks ordered by L2 distance to the query vector (nearest first). Optional filters apply.
func (s *Store) Search(ctx context.Context, queryEmbedding []float32, opts SearchOptions) ([]SearchResult, error) {
	if len(queryEmbedding) != s.dim {
		return nil, fmt.Errorf("embeddings: query embedding length %d != store dimension %d", len(queryEmbedding), s.dim)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	vec := pgvector.NewVector(queryEmbedding)
	var args []interface{}
	argNum := 1
	args = append(args, vec)
	where := []string{}
	if opts.File != "" {
		argNum++
		where = append(where, fmt.Sprintf("file = $%d", argNum))
		args = append(args, opts.File)
	} else if opts.FilePrefix != "" {
		argNum++
		where = append(where, fmt.Sprintf("substring(file from 1 for length($%d::text)) = $%d", argNum, argNum))
		args = append(args, opts.FilePrefix)
	}
	if opts.Lang != "" {
		argNum++
		where = append(where, fmt.Sprintf("lang = $%d", argNum))
		args = append(args, opts.Lang)
	}
	if opts.SymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("symbol_id = $%d", argNum))
		args = append(args, opts.SymbolID)
	}
	if opts.ParentSymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("parent_symbol_id = $%d", argNum))
		args = append(args, opts.ParentSymbolID)
	}
	if opts.RepoID != "" {
		argNum++
		where = append(where, fmt.Sprintf("repo_id = $%d", argNum))
		args = append(args, opts.RepoID)
	}
	if opts.ChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type = $%d", argNum))
		args = append(args, opts.ChunkType)
	}
	if strings.TrimSpace(opts.Module) != "" {
		argNum++
		where = append(where, fmt.Sprintf("COALESCE(chunk_metadata->>'module','') = $%d", argNum))
		args = append(args, strings.TrimSpace(opts.Module))
	}
	metaContains, err := NormalizeMetadataContainsJSON(opts.MetadataContains)
	if err != nil {
		return nil, err
	}
	if len(metaContains) > 0 {
		argNum++
		where = append(where, fmt.Sprintf("chunk_metadata @> $%d::jsonb", argNum))
		args = append(args, metaContains)
	}
	argNum++
	args = append(args, limit)
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}
	q := fmt.Sprintf(`
		SELECT id, content, embedding, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id, chunk_metadata, parent_symbol_id,
		       embedding <-> $1 AS distance
		FROM chunks %s
		ORDER BY embedding <-> $1
		LIMIT $%d`, whereClause, argNum)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var vec pgvector.Vector
		var symbolID *string
		var meta []byte
		var parentID *string
		err := rows.Scan(&r.ID, &r.Content, &vec, &symbolID, &r.File, &r.Lang, &r.ChunkType, &r.StartLine, &r.EndLine, &r.RepoID, &meta, &parentID, &r.Distance)
		if err != nil {
			return nil, err
		}
		r.Embedding = vec.Slice()
		if symbolID != nil {
			r.SymbolID = *symbolID
		}
		if len(meta) > 0 {
			r.MetadataJSON = append([]byte(nil), meta...)
		}
		if parentID != nil {
			r.ParentSymbolID = *parentID
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ListOptions optionally filters chunks by file, symbol, repo, chunk_type, lang, module, or metadata for context retrieval (no vector search).
type ListOptions struct {
	File           string // return only chunks from this file (if set, FilePrefix is ignored)
	FilePrefix     string // return chunks whose file path starts with this prefix (forward slashes)
	SymbolID       string // return only chunks for this symbol
	ParentSymbolID string // return only chunks whose parent_symbol_id matches (container symbol)
	RepoID         string // return only chunks for this repo
	ChunkType      string // e.g. "test", "fixture", "definition"
	Lang           string // e.g. "java", "csharp"
	Limit          int    // max results; 0 = no limit (use with care)
	Module         string // chunk_metadata->>'module' exact match; empty = no filter
	// MetadataContains is a JSON object that must be contained in chunk_metadata (@>).
	MetadataContains []byte
}

// List returns chunks matching the filters, ordered by file and start_line. Use for fetching context by file/symbol.
func (s *Store) List(ctx context.Context, opts ListOptions) ([]Chunk, error) {
	var args []interface{}
	var where []string
	argNum := 0
	if opts.File != "" {
		argNum++
		where = append(where, fmt.Sprintf("file = $%d", argNum))
		args = append(args, opts.File)
	} else if opts.FilePrefix != "" {
		argNum++
		where = append(where, fmt.Sprintf("substring(file from 1 for length($%d::text)) = $%d", argNum, argNum))
		args = append(args, opts.FilePrefix)
	}
	if opts.SymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("symbol_id = $%d", argNum))
		args = append(args, opts.SymbolID)
	}
	if opts.ParentSymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("parent_symbol_id = $%d", argNum))
		args = append(args, opts.ParentSymbolID)
	}
	if opts.RepoID != "" {
		argNum++
		where = append(where, fmt.Sprintf("repo_id = $%d", argNum))
		args = append(args, opts.RepoID)
	}
	if opts.ChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type = $%d", argNum))
		args = append(args, opts.ChunkType)
	}
	if opts.Lang != "" {
		argNum++
		where = append(where, fmt.Sprintf("lang = $%d", argNum))
		args = append(args, opts.Lang)
	}
	if strings.TrimSpace(opts.Module) != "" {
		argNum++
		where = append(where, fmt.Sprintf("COALESCE(chunk_metadata->>'module','') = $%d", argNum))
		args = append(args, strings.TrimSpace(opts.Module))
	}
	listMetaContains, err := NormalizeMetadataContainsJSON(opts.MetadataContains)
	if err != nil {
		return nil, err
	}
	if len(listMetaContains) > 0 {
		argNum++
		where = append(where, fmt.Sprintf("chunk_metadata @> $%d::jsonb", argNum))
		args = append(args, listMetaContains)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}
	q := "SELECT id, content, embedding, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id, chunk_metadata, parent_symbol_id FROM chunks " + whereClause + " ORDER BY file, start_line"
	if opts.Limit > 0 {
		argNum++
		q += fmt.Sprintf(" LIMIT $%d", argNum)
		args = append(args, opts.Limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Chunk
	for rows.Next() {
		var c Chunk
		var vec pgvector.Vector
		var symbolID *string
		var meta []byte
		var parentID *string
		if err := rows.Scan(&c.ID, &c.Content, &vec, &symbolID, &c.File, &c.Lang, &c.ChunkType, &c.StartLine, &c.EndLine, &c.RepoID, &meta, &parentID); err != nil {
			return nil, err
		}
		c.Embedding = vec.Slice()
		if symbolID != nil {
			c.SymbolID = *symbolID
		}
		if len(meta) > 0 {
			c.MetadataJSON = append([]byte(nil), meta...)
		}
		if parentID != nil {
			c.ParentSymbolID = *parentID
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// DeleteByFile removes all chunks for the given file (e.g. before re-indexing).
func (s *Store) DeleteByFile(ctx context.Context, file string) (deleted int64, err error) {
	res, err := s.pool.Exec(ctx, "DELETE FROM chunks WHERE file = $1", file)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

// DeleteByRepo removes all chunks for the given repo_id.
func (s *Store) DeleteByRepo(ctx context.Context, repoID string) (deleted int64, err error) {
	res, err := s.pool.Exec(ctx, "DELETE FROM chunks WHERE repo_id = $1", repoID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

// CountChunksByRepo returns the total number of chunks currently stored for the given repo_id.
// Used after an indexer run completes so callers can report the post-run total alongside the
// per-run delta (Added/Changed/Removed) — see orchestrator.IndexPhaseResult.ChunksTotal (A.7).
// An empty repoID counts all chunks across repos.
func (s *Store) CountChunksByRepo(ctx context.Context, repoID string) (int64, error) {
	var n int64
	if strings.TrimSpace(repoID) == "" {
		err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&n)
		return n, err
	}
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE repo_id = $1`, repoID).Scan(&n)
	return n, err
}

// GetEmbeddingProvider returns the provider and model that produced the current stored vectors (empty if never set).
func (s *Store) GetEmbeddingProvider(ctx context.Context) (*EmbeddingProvider, error) {
	var p EmbeddingProvider
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		"SELECT provider, embedding_model, dimension, updated_at FROM embedding_provider WHERE id = 1",
	).Scan(&p.Provider, &p.EmbeddingModel, &p.Dimension, &updatedAt)
	if err != nil {
		if isNoRows(err) {
			return &EmbeddingProvider{}, nil
		}
		return nil, err
	}
	p.UpdatedAt = updatedAt.Format(time.RFC3339)
	return &p, nil
}

// SetEmbeddingProvider records the provider and model used for the last write (e.g. after InsertChunks).
func (s *Store) SetEmbeddingProvider(ctx context.Context, provider, embeddingModel string, dimension int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE embedding_provider SET provider = $1, embedding_model = $2, dimension = $3, updated_at = NOW() WHERE id = 1`,
		provider, embeddingModel, dimension,
	)
	return err
}

func sanitizeTextForPostgres(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return s
}

func nullUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
