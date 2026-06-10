package indexer

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// buildParentFQMap returns callee fq_name -> best caller fq_name for CONTAINS edges (Phase B).
func buildParentFQMap(parsed *ParsedFile) map[string]string {
	out := make(map[string]string)
	for _, e := range parsed.Edges {
		if !strings.EqualFold(strings.TrimSpace(e.EdgeType), "CONTAINS") {
			continue
		}
		caller := strings.TrimSpace(e.CallerFQName)
		callee := strings.TrimSpace(e.CalleeFQName)
		if caller == "" || callee == "" {
			continue
		}
		if _, ok := out[callee]; ok {
			continue // first caller wins (often MODULE then class)
		}
		out[callee] = caller
	}
	return out
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// signatureHintsLine extracts a short k=v line from signature_json for chunk headers (Phase A).
func signatureHintsLine(sig []byte, budgetRunes int) string {
	if len(sig) == 0 || budgetRunes <= 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(sig, &m); err != nil {
		return ""
	}
	keys := []string{
		"visibility", "exported", "framework",
		"path_pattern", "http_method", "handler_fq", "class_fq",
		"component_sym", "path", "selector", "props_type", "type_text",
	}
	var parts []string
	used := 0
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" || s == "<nil>" {
			continue
		}
		s = strings.ReplaceAll(s, "\n", " ")
		s = truncateRunes(s, 120)
		frag := k + "=" + s
		if used+utf8.RuneCountInString(frag)+1 > budgetRunes {
			break
		}
		parts = append(parts, frag)
		used += utf8.RuneCountInString(frag) + 1
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// buildChunkHeader returns a machine-readable header line (capped). Empty if EnrichChunkContent is false via caller.
func buildChunkHeader(sym ParsedSymbol, file string, parentFQ string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 512
	}
	kind := strings.TrimSpace(sym.Kind)
	fq := strings.TrimSpace(sym.FQName)
	line1 := fmt.Sprintf("[symbol_kind=%s fq_name=%s file=%s lines=%d-%d]", kind, fq, file, sym.StartLine, sym.EndLine)
	if parentFQ != "" {
		line1 += fmt.Sprintf(" [parent_fq=%s]", parentFQ)
	}
	// Reserve ~half for signature hints
	budget := maxRunes - utf8.RuneCountInString(line1)
	if budget < 40 {
		return truncateRunes(line1, maxRunes)
	}
	h2 := signatureHintsLine(sym.SignatureJSON, budget-2)
	if h2 == "" {
		return truncateRunes(line1, maxRunes)
	}
	out := line1 + "\n" + h2
	return truncateRunes(out, maxRunes)
}

func prependChunkHeader(body string, sym ParsedSymbol, file, parentFQ string, cfg ChunkConfig) string {
	if !cfg.EnrichChunkContent {
		return body
	}
	maxH := cfg.MaxChunkHeaderRunes
	if maxH <= 0 {
		maxH = 512
	}
	h := buildChunkHeader(sym, file, parentFQ, maxH)
	if h == "" {
		return body
	}
	return h + "\n\n" + body
}

func chunkMetadataMap(sym ParsedSymbol, file, parentFQ string, chunkType string, chunkIndex int, secondaryRole string, mergedFrom []string, module string) map[string]interface{} {
	meta := map[string]interface{}{
		"symbol_kind": sym.Kind,
		"fq_name":     sym.FQName,
		"file":        file,
		"chunk_type":  chunkType,
		"chunk_index": chunkIndex,
	}
	if parentFQ != "" {
		meta["parent_fq"] = parentFQ
	}
	if secondaryRole != "" {
		meta["chunk_role"] = secondaryRole
	}
	if len(mergedFrom) > 0 {
		meta["merged_symbols"] = mergedFrom
	}
	if m := strings.TrimSpace(module); m != "" {
		meta["module"] = m
	}
	mergeStructuredSignatureIntoChunkMetadata(meta, sym.SignatureJSON)
	return meta
}
