package indexer

import (
	"encoding/json"
	"strings"
)

// ChunkFromParsedFile splits a ParsedFile into chunk plans (symbol-boundary, within token budget).
// repoRoot is the absolute repo path; when empty, Phase C file-backed secondary chunks are skipped.
// Phases A–D: enriched headers, parent fq in metadata, optional secondary chunks, small-symbol merge, chunk_index on splits.
func ChunkFromParsedFile(parsed *ParsedFile, repoID, repoRoot string, cfg ChunkConfig, sanitize SanitizeOptions) []ChunkPlan {
	if cfg.MinTokens <= 0 || cfg.MaxTokens <= 0 {
		cfg = DefaultChunkConfig()
	}
	parentMap := buildParentFQMap(parsed)
	lines := strings.Split(parsed.Source, "\n")

	var raw []rawChunkPart
	for _, sym := range parsed.Symbols {
		chunkTypeBase := "definition"
		if parsed.IsTest {
			chunkTypeBase = "test"
		}
		ct := chunkTypeBase
		if !parsed.IsTest {
			ct = symbolKindToChunkType(sym.Kind)
		}
		content := extractLines(lines, sym.StartLine, sym.EndLine)
		content = Sanitize(content, sanitize)
		tokens := cfg.ApproxTokens(content)
		if tokens <= cfg.MaxTokens {
			raw = append(raw, rawChunkPart{
				sym: sym, chunkType: ct, startLine: sym.StartLine, endLine: sym.EndLine,
				content: content, chunkIndex: 0,
			})
			continue
		}
		raw = append(raw, splitLargeSymbolToRaw(lines, sym, ct, cfg, sanitize)...)
	}

	raw = mergeSmallRawParts(raw, cfg)

	var plans []ChunkPlan
	for _, part := range raw {
		parentFQ := parentMap[part.sym.FQName]
		meta := chunkMetadataMap(part.sym, parsed.Path, parentFQ, part.chunkType, part.chunkIndex, "", part.mergedFrom, parsed.Module)
		metaJSON, _ := json.Marshal(meta)
		content := prependChunkHeader(part.content, part.sym, parsed.Path, parentFQ, cfg)
		plans = append(plans, ChunkPlan{
			Content:      content,
			File:         parsed.Path,
			Lang:         parsed.Lang,
			ChunkType:    part.chunkType,
			StartLine:    part.startLine,
			EndLine:      part.endLine,
			RepoID:       repoID,
			SymbolFQ:     part.sym.FQName,
			SymbolKind:   part.sym.Kind,
			ChunkIndex:   part.chunkIndex,
			ParentFQ:     parentFQ,
			MetadataJSON: metaJSON,
		})
	}

	if sec := secondaryChunkPlans(parsed, repoID, repoRoot, cfg, sanitize); len(sec) > 0 {
		plans = append(plans, sec...)
	}
	return plans
}

func splitLargeSymbolToRaw(
	lines []string,
	sym ParsedSymbol,
	chunkType string,
	cfg ChunkConfig,
	sanitize SanitizeOptions,
) []rawChunkPart {
	ct := chunkType
	if ct == "" {
		ct = symbolKindToChunkType(sym.Kind)
	}
	var out []rawChunkPart
	start := sym.StartLine
	end := sym.EndLine
	targetLines := (cfg.MaxTokens * cfg.CharsPerToken) / 80
	if targetLines < 5 {
		targetLines = 5
	}
	idx := 0
	for start <= end {
		chunkEnd := start + targetLines - 1
		if chunkEnd > end {
			chunkEnd = end
		}
		content := extractLines(lines, start, chunkEnd)
		content = Sanitize(content, sanitize)
		out = append(out, rawChunkPart{
			sym: sym, chunkType: ct, startLine: start, endLine: chunkEnd,
			content: content, chunkIndex: idx,
		})
		idx++
		start = chunkEnd + 1
	}
	return out
}

// extractLines returns the inclusive 1-based line range joined with newlines.
func extractLines(lines []string, startLine, endLine int) string {
	if startLine < 1 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine
	}
	n := len(lines)
	if startLine > n {
		return ""
	}
	if endLine > n {
		endLine = n
	}
	return strings.Join(lines[startLine-1:endLine], "\n")
}

// symbolKindToChunkType maps indexer symbol kinds to embeddings chunk_type (non-test files).
func symbolKindToChunkType(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "api_route", "page_route":
		return "route"
	case "user_flow":
		return "flow"
	case "e2e_spec", "page_object":
		return "e2e_pattern"
	case "form", "test_selector", "ui_test_hook", "static_template":
		return "page"
	case "api_client_request":
		return "api_contract"
	default:
		return "definition"
	}
}
