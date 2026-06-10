package indexer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mergeStructuredSignatureIntoChunkMetadata copies a small, language-agnostic allowlist from
// symbol signature_json into chunk_metadata so SQL filters and hybrid retrieval can use the same
// facts as the metadata symbols table (LSP-style visibility / export surface; route hints).
// Does not overwrite keys already set in meta (indexer-owned fields win).
func mergeStructuredSignatureIntoChunkMetadata(meta map[string]interface{}, sig []byte) {
	if meta == nil {
		return
	}
	var src map[string]interface{}
	if len(sig) > 0 && json.Unmarshal(sig, &src) == nil {
		copySignatureFieldsIntoChunkMeta(meta, src)
	}
	fillRouteHintsNested(meta)
	deriveExportedFromVisibility(meta)
}

func copySignatureFieldsIntoChunkMeta(meta, src map[string]interface{}) {
	stringKeys := []string{
		"visibility", "framework", "http_method", "path_pattern",
		"handler_fq", "class_fq", "component_sym", "selector_kind", "value",
	}
	for _, k := range stringKeys {
		if _, exists := meta[k]; exists {
			continue
		}
		v, ok := src[k]
		if !ok {
			continue
		}
		if s, ok := stringifySignatureScalar(v); ok && s != "" {
			meta[k] = s
		}
	}
	if _, exists := meta["class_fq"]; !exists {
		if v, ok := src["class"]; ok {
			if s, ok := stringifySignatureScalar(v); ok && s != "" {
				meta["class_fq"] = s
			}
		}
	}
	if _, exists := meta["exported"]; !exists {
		if v, ok := src["exported"]; ok {
			if b, ok := signatureTruth(v); ok {
				meta["exported"] = b
			}
		} else if v, ok := src["is_exported"]; ok {
			if b, ok := signatureTruth(v); ok {
				meta["exported"] = b
			}
		}
	}
	if _, exists := meta["static"]; !exists {
		for _, key := range []string{"static", "is_static"} {
			if v, ok := src[key]; ok {
				if b, ok := signatureTruth(v); ok {
					meta["static"] = b
					break
				}
			}
		}
	}
}

func stringifySignatureScalar(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", false
		}
		return s, true
	case float64:
		return strings.TrimSpace(fmt.Sprint(t)), true
	case bool:
		return fmt.Sprint(t), true
	case json.Number:
		return strings.TrimSpace(t.String()), true
	default:
		return "", false
	}
}

func signatureTruth(v interface{}) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "true" || s == "1" {
			return true, true
		}
		if s == "false" || s == "0" {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

func deriveExportedFromVisibility(meta map[string]interface{}) {
	if meta == nil {
		return
	}
	if _, ok := meta["exported"]; ok {
		return
	}
	v, ok := meta["visibility"].(string)
	if !ok {
		return
	}
	meta["exported"] = strings.EqualFold(strings.TrimSpace(v), "public")
}

// fillRouteHintsNested adds route_hints { http_method, path_pattern } when either flat field is set
// (for JSONB @> queries grouped as one object).
func fillRouteHintsNested(meta map[string]interface{}) {
	if meta == nil {
		return
	}
	if _, exists := meta["route_hints"]; exists {
		return
	}
	rh := make(map[string]interface{})
	if s, ok := meta["http_method"].(string); ok && strings.TrimSpace(s) != "" {
		rh["http_method"] = strings.TrimSpace(s)
	}
	if s, ok := meta["path_pattern"].(string); ok && strings.TrimSpace(s) != "" {
		rh["path_pattern"] = strings.TrimSpace(s)
	}
	if len(rh) == 0 {
		return
	}
	meta["route_hints"] = rh
}
