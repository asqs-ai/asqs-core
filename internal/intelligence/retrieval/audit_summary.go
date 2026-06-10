package retrieval

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// retrievalContextAuditSummary builds JSON-friendly fields for audit_log (e.g. retrieve.gap_retrieved).
func retrievalContextAuditSummary(rc *RetrievalContext, canonicalProfile string) map[string]interface{} {
	out := map[string]interface{}{
		"retrieval_profile": canonicalProfile,
	}
	if rc == nil {
		return out
	}
	edgeTypes := make(map[string]struct{})
	for _, d := range rc.Dependencies {
		if d == nil {
			continue
		}
		et := strings.TrimSpace(d.EdgeType)
		et = strings.TrimSuffix(et, "←")
		et = strings.TrimSpace(et)
		if et != "" {
			edgeTypes[et] = struct{}{}
		}
	}
	keys := make([]string, 0, len(edgeTypes))
	for k := range edgeTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 16 {
		keys = keys[:16]
		keys = append(keys, "...(truncated)")
	}
	out["deps_count"] = len(rc.Dependencies)
	out["domain_models_count"] = len(rc.DomainModels)
	out["similar_chunks_count"] = len(rc.SimilarTests)
	segmented, reassembled := similarSegmentationCounts(rc.SimilarTests)
	out["similar_segmented_count"] = segmented
	out["similar_reassembled_count"] = reassembled
	out["related_chunks_count"] = len(rc.RelatedChunks)
	out["fixtures_count"] = len(rc.Fixtures)
	out["config_chunks_count"] = len(rc.Config)
	if rc.ExistingTestCoverage != nil {
		out["existing_tests_detected"] = rc.ExistingTestCoverage.HasExistingTests
		out["covered_branch_intents_count"] = len(rc.ExistingTestCoverage.CoveredIntents)
		out["missing_branch_intents_count"] = len(rc.ExistingTestCoverage.MissingIntents)
	}
	out["dependency_edge_types_sample"] = keys
	return out
}

func similarSegmentationCounts(chunks []*embeddings.Chunk) (segmented, reassembled int) {
	for _, ch := range chunks {
		if ch == nil {
			continue
		}
		if _, ok := chunkEmbeddingSegmentInfo(ch); ok {
			segmented++
		}
		var meta map[string]interface{}
		if len(ch.MetadataJSON) > 0 && json.Unmarshal(ch.MetadataJSON, &meta) == nil {
			if v, ok := meta["retrieval_reassembled"].(bool); ok && v {
				reassembled++
			}
		}
	}
	return segmented, reassembled
}
