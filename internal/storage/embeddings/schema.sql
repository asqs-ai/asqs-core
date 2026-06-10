-- Requires: CREATE EXTENSION vector (done in Open/InitSchema)
-- Chunks table: content + embedding + provenance for symbol-aware RAG
CREATE TABLE IF NOT EXISTS chunks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content     TEXT NOT NULL,
    embedding   vector(1536) NOT NULL,
    symbol_id   UUID,
    file        TEXT NOT NULL,
    lang        TEXT NOT NULL,
    chunk_type  TEXT NOT NULL DEFAULT 'definition',
    start_line  INTEGER NOT NULL DEFAULT 0,
    end_line    INTEGER NOT NULL DEFAULT 0,
    repo_id     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_chunks_file ON chunks (file);
CREATE INDEX IF NOT EXISTS idx_chunks_lang ON chunks (lang);
CREATE INDEX IF NOT EXISTS idx_chunks_symbol ON chunks (symbol_id) WHERE symbol_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_chunks_repo ON chunks (repo_id) WHERE repo_id != '';
CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks (chunk_type);

-- Optional JSON metadata for chunking phases A–D (symbol_kind, fq_name, chunk_index, parent_fq, chunk_role, …)
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS chunk_metadata JSONB;

-- Denormalized container symbol (metadata.symbols.id) for fast “chunks under module/class” queries when DB is shared with metadata.
-- No FK here: embeddings may live on a separate Postgres instance. When co-located, integrity is ensured by reindex.
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS parent_symbol_id UUID;

CREATE INDEX IF NOT EXISTS idx_chunks_repo_type_lang ON chunks (repo_id, chunk_type, lang);

CREATE INDEX IF NOT EXISTS idx_chunks_parent_symbol ON chunks (parent_symbol_id) WHERE parent_symbol_id IS NOT NULL;

-- HNSW index for approximate nearest neighbor (L2). Use: ORDER BY embedding <-> $1
CREATE INDEX IF NOT EXISTS idx_chunks_embedding_hnsw ON chunks
    USING hnsw (embedding vector_l2_ops);

-- Single row: which embedding provider/model produced the current vectors (for reindex / provider switch).
CREATE TABLE IF NOT EXISTS embedding_provider (
    id              INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    provider        TEXT NOT NULL DEFAULT '',
    embedding_model TEXT NOT NULL DEFAULT '',
    dimension       INTEGER NOT NULL DEFAULT 1536,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO embedding_provider (id, provider, embedding_model, dimension)
VALUES (1, '', '', 1536)
ON CONFLICT (id) DO NOTHING;
