package retrieval

import (
	"encoding/json"
	"strings"
)

// chunkModuleFromMetadataJSON returns the indexer "module" field from chunk_metadata when present.
func chunkModuleFromMetadataJSON(meta []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	v, ok := m["module"]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// hybridModuleWidenThreshold returns the minimum number of vector hits before we skip widening (relax module filter).
func hybridModuleWidenThreshold(poolSize int) int {
	n := poolSize / 8
	if n < 2 {
		n = 2
	}
	return n
}

// shouldWidenHybridModuleSearch is true when strict module-filtered vector search returned too few neighbors.
func shouldWidenHybridModuleSearch(strictCount, poolSize int) bool {
	return strictCount < hybridModuleWidenThreshold(poolSize)
}
