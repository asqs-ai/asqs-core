package indexer

import (
	"strings"
)

// mergeableSmallKinds are merged when MergeSmallSymbols is on (Phase D).
var mergeableSmallKinds = map[string]struct{}{
	"nest_guard":               {},
	"nest_pipe":                {},
	"nest_interceptor":         {},
	"dto":                      {},
	"angular_template_binding": {},
	"react_hook":               {},
	"test_selector":            {},
}

func isMergeableSymbolKind(kind string) bool {
	_, ok := mergeableSmallKinds[strings.ToLower(strings.TrimSpace(kind))]
	return ok
}

type rawChunkPart struct {
	sym        ParsedSymbol
	chunkType  string
	startLine  int
	endLine    int
	content    string
	chunkIndex int
	mergedFrom []string
}

// mergeSmallRawParts coalesces adjacent tiny symbols of mergeable kinds (Phase D).
func mergeSmallRawParts(parts []rawChunkPart, cfg ChunkConfig) []rawChunkPart {
	if !cfg.MergeSmallSymbols || len(parts) <= 1 {
		return parts
	}
	threshold := cfg.MinTokens / 4
	if threshold < 40 {
		threshold = 40
	}
	var out []rawChunkPart
	i := 0
	for i < len(parts) {
		cur := parts[i]
		if !isMergeableSymbolKind(cur.sym.Kind) || cfg.ApproxTokens(cur.content) >= threshold {
			out = append(out, cur)
			i++
			continue
		}
		primarySym := cur.sym
		chunkType := cur.chunkType
		startLine := cur.startLine
		endLine := cur.endLine
		content := cur.content
		mergedFrom := []string{cur.sym.FQName}
		j := i + 1
		for j < len(parts) {
			next := parts[j]
			if chunkType != next.chunkType {
				break
			}
			if !isMergeableSymbolKind(next.sym.Kind) {
				break
			}
			if next.startLine > endLine+2 {
				break
			}
			combined := content + "\n\n" + next.content
			if cfg.ApproxTokens(combined) > cfg.MinTokens {
				break
			}
			content = combined
			endLine = next.endLine
			mergedFrom = append(mergedFrom, next.sym.FQName)
			j++
		}
		rc := rawChunkPart{
			sym:        primarySym,
			chunkType:  chunkType,
			startLine:  startLine,
			endLine:    endLine,
			content:    content,
			chunkIndex: cur.chunkIndex,
		}
		if len(mergedFrom) > 1 {
			rc.mergedFrom = mergedFrom
		}
		out = append(out, rc)
		i = j
	}
	return out
}
