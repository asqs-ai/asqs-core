package retrieval

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// ContextCompactOptions configures deterministic compaction of retrieval chunks before BuildLLMContext.
// Default (zero): disabled. When Enabled, the orchestrator compacts each plan item's RetrievalContext once
// before parallel test/doc generation (shared Context pointer).
//
// Rationale: long multi-chunk contexts increase latency and cost; redundant boilerplate and overlapping
// same-file dependency snippets add noise (see Liu et al., "Lost in the Middle"; Gao et al., RAG survey — DOCUMENTATION.md).
type ContextCompactOptions struct {
	Enabled bool
	// MaxNonTargetChunkRunes caps UTF-8 runes in each non-target chunk body after merge/dedupe; 0 = DefaultCompactMaxNonTargetChunkRunes.
	MaxNonTargetChunkRunes int
	// MergeSameFileDependencies collapses multiple dependency edges whose symbols share the same file into one edge
	// with merged chunk text (preamble lists all FQNs). Target method/class are never merged here.
	MergeSameFileDependencies bool
	// DedupeImportBoilerplate strips duplicate leading package/import/using/export blocks (per language heuristic)
	// after the first occurrence in traversal order (dependencies → domain → related → similar → fixtures → config).
	DedupeImportBoilerplate bool
	// MaxBoilerplateScanRunes limits how far from each chunk start we scan for that header block; 0 = DefaultCompactBoilerplateScanRunes when DedupeImportBoilerplate.
	MaxBoilerplateScanRunes int
}

const (
	// DefaultCompactMaxNonTargetChunkRunes is applied when Enabled and MaxNonTargetChunkRunes <= 0.
	DefaultCompactMaxNonTargetChunkRunes = 4096
	// DefaultCompactBoilerplateScanRunes caps header detection scan when MaxBoilerplateScanRunes <= 0.
	DefaultCompactBoilerplateScanRunes = 2048
)

func (o ContextCompactOptions) effectiveMaxRunes() int {
	if o.MaxNonTargetChunkRunes <= 0 {
		return DefaultCompactMaxNonTargetChunkRunes
	}
	return o.MaxNonTargetChunkRunes
}

func (o ContextCompactOptions) effectiveBoilerplateScan() int {
	if !o.DedupeImportBoilerplate {
		return 0
	}
	if o.MaxBoilerplateScanRunes <= 0 {
		return DefaultCompactBoilerplateScanRunes
	}
	return o.MaxBoilerplateScanRunes
}

// ContextCompactStats summarizes a single compaction pass (audit / tests).
type ContextCompactStats struct {
	InputContentRunes          int64
	OutputContentRunes         int64
	MergedDependencyFileGroups int
	DedupedBoilerplateChunks   int
	TruncatedChunks            int
}

// CompactRetrievalContext mutates rc in place: replaces chunk pointers where content changes.
// Target method/class chunks and FailureHint are never modified. Idempotent enough for a second call:
// same-file groups are already merged; truncated bodies stay within the cap.
func CompactRetrievalContext(rc *RetrievalContext, o ContextCompactOptions) ContextCompactStats {
	var stats ContextCompactStats
	if rc == nil || !o.Enabled {
		return stats
	}
	stats.InputContentRunes = countRetrievalContextContentRunes(rc)
	maxR := o.effectiveMaxRunes()
	scanR := o.effectiveBoilerplateScan()

	if o.MergeSameFileDependencies && len(rc.Dependencies) > 0 {
		rc.Dependencies, stats.MergedDependencyFileGroups = mergeDependencyEdgesByFile(rc.Dependencies, maxR, &stats)
	}

	var seen map[string]bool
	if scanR > 0 {
		seen = make(map[string]bool)
	}

	for _, d := range rc.Dependencies {
		if d != nil {
			d.Chunk = dedupeAndTruncateChunk(d.Chunk, scanR, seen, maxR, &stats)
		}
	}
	for _, dm := range rc.DomainModels {
		if dm != nil {
			dm.Chunk = dedupeAndTruncateChunk(dm.Chunk, scanR, seen, maxR, &stats)
		}
	}
	rc.RelatedChunks = mapChunkSlice(rc.RelatedChunks, scanR, seen, maxR, &stats)
	rc.SimilarTests = mapChunkSlice(rc.SimilarTests, scanR, seen, maxR, &stats)
	rc.Fixtures = mapChunkSlice(rc.Fixtures, scanR, seen, maxR, &stats)
	rc.Config = mapChunkSlice(rc.Config, scanR, seen, maxR, &stats)

	stats.OutputContentRunes = countRetrievalContextContentRunes(rc)
	return stats
}

func countRetrievalContextContentRunes(rc *RetrievalContext) int64 {
	if rc == nil {
		return 0
	}
	var n int64
	n += int64(utf8.RuneCountInString(rc.FailureHint))
	add := func(c *embeddings.Chunk) {
		if c != nil {
			n += int64(utf8.RuneCountInString(c.Content))
		}
	}
	if rc.TargetMethod != nil {
		add(rc.TargetMethod.Chunk)
	}
	if rc.TargetClass != nil {
		add(rc.TargetClass.Chunk)
	}
	for _, d := range rc.Dependencies {
		if d != nil {
			add(d.Chunk)
		}
	}
	for _, dm := range rc.DomainModels {
		if dm != nil {
			add(dm.Chunk)
		}
	}
	for _, c := range rc.RelatedChunks {
		add(c)
	}
	for _, c := range rc.SimilarTests {
		add(c)
	}
	for _, c := range rc.Fixtures {
		add(c)
	}
	for _, c := range rc.Config {
		add(c)
	}
	return n
}

func cloneChunk(c *embeddings.Chunk) *embeddings.Chunk {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

func truncateRunes(s string, maxRunes int) (out string, truncated bool) {
	if maxRunes <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s, false
	}
	return string(r[:maxRunes]) + "\n... (truncated)", true
}

func dedupeAndTruncateChunk(c *embeddings.Chunk, scanR int, seen map[string]bool, maxR int, stats *ContextCompactStats) *embeddings.Chunk {
	if c == nil {
		return nil
	}
	content := c.Content
	if scanR > 0 && seen != nil {
		prefix, fp := extractLeadingHeaderBlock(content, c.Lang, scanR)
		if fp != "" {
			if seen[fp] {
				content = content[len(prefix):]
				stats.DedupedBoilerplateChunks++
			} else {
				seen[fp] = true
			}
		}
	}
	if t, did := truncateRunes(content, maxR); did {
		stats.TruncatedChunks++
		nc := cloneChunk(c)
		nc.Content = t
		return nc
	}
	if content != c.Content {
		nc := cloneChunk(c)
		nc.Content = content
		return nc
	}
	return c
}

func mapChunkSlice(chunks []*embeddings.Chunk, scanR int, seen map[string]bool, maxR int, stats *ContextCompactStats) []*embeddings.Chunk {
	if len(chunks) == 0 {
		return chunks
	}
	out := make([]*embeddings.Chunk, len(chunks))
	for i, c := range chunks {
		out[i] = dedupeAndTruncateChunk(c, scanR, seen, maxR, stats)
	}
	return out
}

func depFileKey(e *DependencyEdge) string {
	if e == nil || e.Symbol == nil {
		return ""
	}
	f := filepath.ToSlash(strings.TrimSpace(e.Symbol.File))
	if f == "" {
		return "\x00sym:" + e.Symbol.ID
	}
	return f
}

func mergeDependencyEdgesByFile(edges []*DependencyEdge, maxRunes int, stats *ContextCompactStats) ([]*DependencyEdge, int) {
	if len(edges) <= 1 {
		return edges, 0
	}
	groups := make(map[string][]*DependencyEdge)
	order := make([]string, 0)
	for _, e := range edges {
		if e == nil || e.Symbol == nil {
			continue
		}
		k := depFileKey(e)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	mergedHead := make(map[string]*DependencyEdge)
	mergedGroups := 0
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			mergedHead[k] = g[0]
			continue
		}
		mergedGroups++
		mergedHead[k] = mergeDependencyGroup(g, maxRunes, stats)
	}
	seen := make(map[string]bool)
	out := make([]*DependencyEdge, 0, len(edges))
	for _, e := range edges {
		if e == nil || e.Symbol == nil {
			out = append(out, e)
			continue
		}
		k := depFileKey(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, mergedHead[k])
	}
	return out, mergedGroups
}

func chunkStartLine(c *embeddings.Chunk) int {
	if c == nil || c.StartLine <= 0 {
		return 1 << 29
	}
	return c.StartLine
}

func mergeDependencyGroup(g []*DependencyEdge, maxRunes int, stats *ContextCompactStats) *DependencyEdge {
	sort.SliceStable(g, func(i, j int) bool {
		li, lj := chunkStartLine(g[i].Chunk), chunkStartLine(g[j].Chunk)
		if li != lj {
			return li < lj
		}
		var fi, fj string
		if g[i].Symbol != nil {
			fi = g[i].Symbol.FQName
		}
		if g[j].Symbol != nil {
			fj = g[j].Symbol.FQName
		}
		return fi < fj
	})
	first := g[0]
	var names []string
	var parts []string
	minL, maxL := int(^uint(0)>>1), 0
	hasLines := false
	for _, e := range g {
		if e.Symbol != nil {
			names = append(names, e.Symbol.FQName)
		}
		if e.Chunk != nil && strings.TrimSpace(e.Chunk.Content) != "" {
			parts = append(parts, strings.TrimSpace(e.Chunk.Content))
			if e.Chunk.StartLine > 0 {
				hasLines = true
				if e.Chunk.StartLine < minL {
					minL = e.Chunk.StartLine
				}
				if e.Chunk.EndLine > maxL {
					maxL = e.Chunk.EndLine
				}
			}
		}
	}
	preamble := "// ASQS merged same-file dependencies: " + strings.Join(names, ", ") + "\n\n"
	merged := preamble + strings.Join(parts, "\n\n// ---\n\n")
	if t, did := truncateRunes(merged, maxRunes); did {
		merged = t
		stats.TruncatedChunks++
	}
	var ch *embeddings.Chunk
	if first.Chunk != nil {
		ch = cloneChunk(first.Chunk)
	} else {
		ch = &embeddings.Chunk{}
		if first.Symbol != nil {
			ch.File = first.Symbol.File
			ch.Lang = first.Symbol.Lang
		}
	}
	ch.Content = merged
	if hasLines && minL <= maxL {
		ch.StartLine = minL
		ch.EndLine = maxL
	}
	edgeType := first.EdgeType
	for _, e := range g[1:] {
		if e.EdgeType != edgeType {
			edgeType = "uses"
			break
		}
	}
	return &DependencyEdge{
		SymbolChunk: SymbolChunk{Symbol: first.Symbol, Chunk: ch},
		EdgeType:    edgeType,
	}
}

// extractLeadingHeaderBlock returns the exact byte prefix of s that forms a boilerplate header and a
// normalized fingerprint for deduplication. Empty fingerprint means no header detected.
func extractLeadingHeaderBlock(s, lang string, maxScanRunes int) (prefix string, fingerprint string) {
	if s == "" || maxScanRunes <= 0 {
		return "", ""
	}
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	lang = strings.ToLower(strings.TrimSpace(lang))

	var b strings.Builder
	var fpLines []string
	runeBudget := maxScanRunes

	writeLine := func(line string) bool {
		ru := utf8.RuneCountInString(line) + 1
		if ru > runeBudget {
			return false
		}
		b.WriteString(line)
		b.WriteByte('\n')
		runeBudget -= ru
		return true
	}

	inGoImport := false
	startedHeader := false
	i := 0
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimSpace(line)

		if !startedHeader {
			if trim == "" {
				i++
				continue
			}
			if strings.HasPrefix(trim, "//") {
				if !writeLine(line) {
					break
				}
				i++
				continue
			}
		}

		if inGoImport {
			if !writeLine(line) {
				break
			}
			fpLines = append(fpLines, trim)
			if trim == ")" || strings.HasPrefix(trim, ") ") {
				inGoImport = false
			}
			i++
			continue
		}

		// Blank lines between package and import (Java/Go) remain part of the header block.
		if startedHeader && trim == "" {
			if !writeLine(line) {
				break
			}
			i++
			continue
		}

		ok, goImportOpen := isBoilerplateHeaderLine(trim, lang)
		if ok {
			if !writeLine(line) {
				break
			}
			fpLines = append(fpLines, trim)
			startedHeader = true
			if goImportOpen {
				inGoImport = true
			}
			i++
			continue
		}

		if startedHeader {
			break
		}
		if trim != "" {
			return "", ""
		}
		i++
	}
	if len(fpLines) == 0 {
		return "", ""
	}
	return b.String(), strings.Join(fpLines, "\n")
}

func isBoilerplateHeaderLine(trim string, lang string) (ok bool, goImportParen bool) {
	switch lang {
	case "go":
		if strings.HasPrefix(trim, "package ") {
			return true, false
		}
		if strings.HasPrefix(trim, "import ") {
			if strings.Contains(trim, "(") {
				return true, true
			}
			return true, false
		}
	case "java", "kotlin":
		if strings.HasPrefix(trim, "package ") || strings.HasPrefix(trim, "import ") {
			return true, false
		}
	case "csharp", "cs":
		if strings.HasPrefix(trim, "using ") || strings.HasPrefix(trim, "global using ") {
			return true, false
		}
	case "python", "py":
		if strings.HasPrefix(trim, "import ") || strings.HasPrefix(trim, "from ") {
			return true, false
		}
	case "javascript", "typescript", "js", "ts":
		switch trim {
		case `"use strict"`, `'use strict'`, `"use client"`, `'use client'`:
			return true, false
		}
		if strings.HasPrefix(trim, "import ") || strings.HasPrefix(trim, "export ") {
			return true, false
		}
	default:
		if strings.HasPrefix(trim, "package ") || strings.HasPrefix(trim, "import ") ||
			strings.HasPrefix(trim, "using ") || strings.HasPrefix(trim, "global using ") ||
			strings.HasPrefix(trim, "from ") {
			return true, false
		}
	}
	return false, false
}
