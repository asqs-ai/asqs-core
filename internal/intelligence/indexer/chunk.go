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
	idx := 0
	for start <= end {
		chunkEnd := lastLineWithinBudget(lines, start, end, cfg)
		content := extractLines(lines, start, chunkEnd)
		content = Sanitize(content, sanitize)
		out = append(out, rawChunkPart{
			sym: sym, chunkType: ct, startLine: start, endLine: chunkEnd,
			content: content, chunkIndex: idx,
		})
		idx++
		if chunkEnd >= end {
			break
		}
		start = nextSplitStart(start, chunkEnd, cfg)
	}
	return out
}

// lastLineWithinBudget returns the highest line L in [start, end] whose content from start through L
// still fits MaxTokens — or start itself when even one line exceeds the budget.
//
// This measures the content. The previous version computed a fixed line count:
//
//	targetLines := (cfg.MaxTokens * cfg.CharsPerToken) / 80
//
// which hardcodes 80 characters per line and so ignores the config it is derived from. On minified
// or generated sources — a single line can be thousands of characters — that produced chunks many
// times MaxTokens, which the embedding provider then rejected or truncated. On heavily indented or
// declaration-dense code it produced chunks far under MinTokens, wasting the budget the config asked
// for. ApproxTokens is the same estimator the caller already uses to decide whether a symbol needs
// splitting at all, so this makes the split path agree with the decision that reached it.
func lastLineWithinBudget(lines []string, start, end int, cfg ChunkConfig) int {
	budget := cfg.MaxTokens
	if budget <= 0 {
		budget = DefaultChunkConfig().MaxTokens
	}
	last := start
	for line := start; line <= end; line++ {
		if cfg.ApproxTokens(extractLines(lines, start, line)) > budget && line > start {
			break
		}
		last = line
	}
	return last
}

// nextSplitStart returns where the following chunk begins, backing up by roughly 10% of the chunk
// just emitted so consecutive chunks overlap.
//
// Without overlap a method split at line 40 puts the loop header in one chunk and its body in the
// next, and neither chunk is self-contained: retrieval that surfaces the second one shows the model
// a body whose condition it cannot see. The oversize embedding fallback has always overlapped for
// this reason; the primary path did not, so which behaviour a symbol got depended on whether it
// happened to exceed the provider's input limit.
//
// The step is always at least one line, so a chunk that is a single long line cannot loop forever.
func nextSplitStart(start, chunkEnd int, cfg ChunkConfig) int {
	span := chunkEnd - start + 1
	overlap := span / 10
	if overlap < 1 {
		overlap = 1
	}
	// Never re-emit the whole chunk: the next start must advance.
	if overlap >= span {
		overlap = span - 1
	}
	next := chunkEnd + 1 - overlap
	if next <= start {
		next = start + 1
	}
	return next
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
