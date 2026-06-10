package retrieval

import (
	"context"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// SimilarReferenceRankedChunks runs the same similar-reference ranking pipeline as Retrieve
// (per-profile chunk types, hybrid vector search with optional module filter and adaptive widening,
// pool dedupe by stable key, then MMR — Carbonell & Goldstein, SIGIR 1998).
//
// It is intended for offline IR evaluation (nDCG, MRR) against labeled chunk IDs; production code
// continues to call gatherSimilarReferenceChunks via Retrieve.
//
// target must be non-nil (typically the target method/symbol chunk whose embedding is the query).
// fileModule is the optional module string used for hybrid filtering (e.g. metadata.files.module);
// when empty, module is taken from target chunk metadata when present.
func SimilarReferenceRankedChunks(ctx context.Context, chunks ChunkReader, target *embeddings.Chunk, req ContextRequest, fileModule string) []*embeddings.Chunk {
	if target == nil || chunks == nil {
		return nil
	}
	return gatherSimilarReferenceChunks(ctx, chunks, target, req, fileModule)
}
