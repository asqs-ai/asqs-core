package indexer

import (
	"strconv"
	"strings"
)

// openAPIResolveInternalRefs expands in-document $ref on Path Item and Operation objects (OpenAPI 3.x /
// Swagger 2 style) using JSON Pointer (RFC 6901). External refs (no leading #) are left unresolved.
// Cycles and depth over maxOpenAPIRefDepth are capped to keep indexing robust.

const maxOpenAPIRefDepth = 24

func jsonPointerResolve(doc interface{}, ref string) interface{} {
	pointer := ref
	if strings.HasPrefix(pointer, "#") {
		pointer = pointer[1:]
	}
	if pointer == "" {
		return doc
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil
	}
	parts := strings.Split(pointer, "/")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	cur := doc
	for _, raw := range parts {
		tok := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]interface{}:
			nx, ok := v[tok]
			if !ok {
				return nil
			}
			cur = nx
		case []interface{}:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil
			}
			cur = v[idx]
		default:
			return nil
		}
	}
	return cur
}

func shallowMergeOpenAPI(base, overlay map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if k == "$ref" {
			continue
		}
		out[k] = v
	}
	return out
}

func resolveOpenAPIPathItem(val interface{}, root map[string]interface{}, depth int, seen map[string]bool) map[string]interface{} {
	m, ok := val.(map[string]interface{})
	if !ok {
		return nil
	}
	m = shallowMergeOpenAPI(m, map[string]interface{}{})
	for depth < maxOpenAPIRefDepth {
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#") {
			break
		}
		if seen[ref] {
			break
		}
		seen[ref] = true
		target := jsonPointerResolve(root, ref)
		seen[ref] = false
		tm, ok := target.(map[string]interface{})
		if !ok {
			break
		}
		m = shallowMergeOpenAPI(tm, m)
		delete(m, "$ref")
		depth++
	}
	for vk, vv := range m {
		vlow := strings.ToLower(strings.TrimSpace(vk))
		if _, isVerb := openapiHTTPVerbs[vlow]; !isVerb {
			continue
		}
		if om, ok := vv.(map[string]interface{}); ok {
			m[vk] = resolveOpenAPIOperation(om, root, depth+1, seen)
		}
	}
	return m
}

func resolveOpenAPIOperation(op map[string]interface{}, root map[string]interface{}, depth int, seen map[string]bool) map[string]interface{} {
	m := shallowMergeOpenAPI(op, map[string]interface{}{})
	for depth < maxOpenAPIRefDepth {
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#") {
			break
		}
		if seen[ref] {
			break
		}
		seen[ref] = true
		target := jsonPointerResolve(root, ref)
		seen[ref] = false
		tm, ok := target.(map[string]interface{})
		if !ok {
			break
		}
		m = shallowMergeOpenAPI(tm, m)
		delete(m, "$ref")
		depth++
	}
	return m
}

func expandOpenAPIPathRefs(paths map[string]interface{}, root map[string]interface{}) map[string]interface{} {
	if paths == nil {
		return nil
	}
	out := make(map[string]interface{}, len(paths))
	for k, v := range paths {
		resolved := resolveOpenAPIPathItem(v, root, 0, make(map[string]bool))
		if resolved != nil {
			out[k] = resolved
		} else {
			out[k] = v
		}
	}
	return out
}

func openapiPathsMapToOps(paths map[string]interface{}) []openapiOp {
	if paths == nil {
		return nil
	}
	var out []openapiOp
	for pth, item := range paths {
		if pth == "" || !strings.HasPrefix(pth, "/") {
			continue
		}
		methods, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		for verb, opVal := range methods {
			vlow := strings.ToLower(strings.TrimSpace(verb))
			if _, ok := openapiHTTPVerbs[vlow]; !ok {
				continue
			}
			opID := ""
			if opMap, ok := opVal.(map[string]interface{}); ok {
				if oid, ok := opMap["operationId"].(string); ok {
					opID = strings.TrimSpace(oid)
				}
			}
			out = append(out, openapiOp{
				method:      strings.ToUpper(vlow),
				path:        pth,
				operationID: opID,
			})
		}
	}
	return out
}
