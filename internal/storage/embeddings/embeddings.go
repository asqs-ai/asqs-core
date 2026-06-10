// Package embeddings provides a vector store for source code chunks and embeddings using pgvector.
//
// Store chunks with content, embedding vector, and provenance (file, symbol_id, optional
// parent_symbol_id for CONTAINS parent, lang, chunk_type, chunk_metadata JSONB) for symbol-aware RAG.
// Use Open and InitSchema to create the table and HNSW index, then InsertChunk/InsertChunks to index,
// and Search/List to query with optional filters (file, lang, symbol_id, parent_symbol_id, repo_id, chunk_type).
// Dimension defaults to 1536 (e.g. OpenAI).
package embeddings
