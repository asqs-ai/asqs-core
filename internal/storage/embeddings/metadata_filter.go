package embeddings

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeMetadataContainsJSON returns compact JSON for a metadata containment filter, or nil if empty.
// The store uses PostgreSQL `chunk_metadata @> $n::jsonb`; input must be a JSON object (not array/scalar).
func NormalizeMetadataContainsJSON(b []byte) ([]byte, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, nil
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("embeddings: metadata_contains is not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v map[string]interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("embeddings: metadata_contains must be a JSON object: %w", err)
	}
	if v == nil {
		return nil, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}
