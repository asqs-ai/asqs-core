package overview

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// overviewIgnoreDirs are directory names excluded from overview docs and dependency diagrams (output/build/cache).
var overviewIgnoreDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, "out": true, "target": true,
	".next": true, ".nuxt": true, ".output": true, ".svelte-kit": true, ".astro": true,
	"coverage": true, ".nx": true, ".angular": true, ".turbo": true, ".vite": true,
	".parcel-cache": true, ".cache": true, ".serverless": true,
}

// isOverviewIgnoredPath returns true if path contains any ignored segment (e.g. dist/, target/, node_modules/).
// canonicalOverviewRepoPath collapses equivalent spellings of the same repo-relative file
// (e.g. "./foo.ts" vs "foo.ts") so the dependency graph does not get duplicate nodes.
func canonicalOverviewRepoPath(p string) string {
	s := strings.TrimSpace(filepath.ToSlash(p))
	s = strings.TrimPrefix(s, "./")
	if s == "" {
		return ""
	}
	c := path.Clean(s)
	if c == "." {
		return ""
	}
	return c
}

const mermaidDepLabelMaxRunes = 88

func mermaidFileDepNodeLabel(repoRel string) string {
	repoRel = canonicalOverviewRepoPath(repoRel)
	if repoRel == "" {
		return ""
	}
	r := []rune(repoRel)
	if len(r) <= mermaidDepLabelMaxRunes {
		return escapeMermaidLabel(repoRel)
	}
	head := string(r[:40])
	tail := string(r[len(r)-40:])
	return escapeMermaidLabel(head + "…" + tail)
}

func isOverviewIgnoredPath(path string) bool {
	path = filepath.ToSlash(strings.Trim(path, "/"))
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		if overviewIgnoreDirs[part] {
			return true
		}
	}
	return false
}

// overviewCanonicalLang maps workflow/indexer spellings to the lang column stored in metadata (files/symbols).
func overviewCanonicalLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "cs":
		return "csharp"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	default:
		return strings.TrimSpace(lang)
	}
}

// BuildOverviewVisualSections returns Markdown sections to append to the overview doc: (1) a module/file
// structure diagram (when there are files), (2) a file dependency graph from AST edges when present.
// Both are Mermaid flowcharts that render in GitHub and most Markdown viewers.
func BuildOverviewVisualSections(ctx context.Context, meta *metadata.Store, lang, repoRoot, overviewPath string) string {
	if meta == nil || lang == "" {
		return ""
	}
	lang = overviewCanonicalLang(lang)
	var b strings.Builder

	// Module and file structure (Mermaid)
	if mermaid := buildModuleFileStructureMermaid(ctx, meta, lang); mermaid != "" {
		b.WriteString("\n\n## Module and file structure\n\n")
		b.WriteString("Files grouped by module (from the index).\n\n")
		b.WriteString(mermaid)
	}

	// File dependency graph (Mermaid); uses all indexed languages (not only dominant workflow lang).
	// Empty-state copy follows workflow lang so JS/TS overviews are not told to enable the Java advanced JAR.
	b.WriteString(buildFileDependencySectionMermaid(ctx, meta, lang))
	out := b.String()
	if out == "" {
		return ""
	}
	// Marker lets later runs strip and replace only the auto-generated appendix while keeping narrative prose.
	return OverviewVisualAppendixMarker + "\n" + strings.TrimLeft(out, "\n")
}

const mermaidMaxNodesPerDiagram = 8 // max file nodes per diagram in hierarchical layout

// buildModuleFileStructureMermaid returns Mermaid graph TD blocks with hierarchical layout: module → directory (package) → files. Uses TB direction for vertical hierarchy.
func buildModuleFileStructureMermaid(ctx context.Context, meta *metadata.Store, lang string) string {
	isTest := false
	files, err := meta.ListFiles(ctx, lang, &isTest)
	if err != nil || len(files) == 0 {
		return ""
	}
	// byModule[mod][dir] = file paths (dir = last path component before file, e.g. "model", "owner")
	byModule := make(map[string]map[string][]string)
	for _, f := range files {
		if isOverviewIgnoredPath(f.File) {
			continue
		}
		mod := f.Module
		if mod == "" {
			mod = "(default)"
		}
		dir := filepath.Base(filepath.Dir(filepath.FromSlash(f.File)))
		if byModule[mod] == nil {
			byModule[mod] = make(map[string][]string)
		}
		byModule[mod][dir] = append(byModule[mod][dir], f.File)
	}
	var modOrder []string
	for mod := range byModule {
		modOrder = append(modOrder, mod)
	}
	sort.Strings(modOrder)
	var b strings.Builder
	mermaidInit := "%%{init: {'themeVariables': {'fontSize': '20px'}, 'flowchart': {'nodeSpacing': 80, 'rankSpacing': 60}}}%%\n"
	for _, mod := range modOrder {
		modLabel := mod
		if modLabel == "(default)" {
			modLabel = "default"
		}
		modLabelEsc := escapeMermaidLabel(modLabel)
		modID := "mod_" + escapeMermaidID(mod)
		if mod == "(default)" {
			modID = "mod_default"
		}
		dirsMap := byModule[mod]
		var dirOrder []string
		for d := range dirsMap {
			dirOrder = append(dirOrder, d)
		}
		sort.Strings(dirOrder)
		for _, dir := range dirOrder {
			paths := dirsMap[dir]
			dirID := "dir_" + escapeMermaidID(dir)
			dirLabelEsc := escapeMermaidLabel(dir)
			for chunkStart := 0; chunkStart < len(paths); chunkStart += mermaidMaxNodesPerDiagram {
				chunkEnd := chunkStart + mermaidMaxNodesPerDiagram
				if chunkEnd > len(paths) {
					chunkEnd = len(paths)
				}
				chunk := paths[chunkStart:chunkEnd]
				if len(modOrder) > 1 || len(dirsMap) > 1 || chunkStart > 0 {
					b.WriteString(fmt.Sprintf("\n### Module: %s — %s", modLabelEsc, dirLabelEsc))
					if chunkStart > 0 {
						b.WriteString(fmt.Sprintf(" (files %d–%d)", chunkStart+1, chunkEnd))
					}
					b.WriteString("\n\n")
				}
				b.WriteString("```mermaid\n")
				b.WriteString(mermaidInit)
				b.WriteString("graph TD\n")
				b.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", modID, modLabelEsc))
				b.WriteString(fmt.Sprintf("    %s --> %s\n", modID, dirID))
				b.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", dirID, dirLabelEsc))
				for i := range chunk {
					id := fmt.Sprintf("n%d", i)
					b.WriteString(fmt.Sprintf("    %s --> %s\n", dirID, id))
				}
				for i, p := range chunk {
					id := fmt.Sprintf("n%d", i)
					label := escapeMermaidLabel(filepath.Base(p))
					b.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", id, label))
				}
				b.WriteString("```\n\n")
			}
		}
	}
	return b.String()
}

func escapeMermaidID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
}

// escapeMermaidLabel escapes text for Mermaid node/subgraph labels in ["..."].
// Avoids characters that break parsing in various Mermaid versions.
func escapeMermaidLabel(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`"`, `#quot;`,
		`(`, ` `,
		`)`, ` `,
		`[`, ` `,
		`]`, ` `,
	).Replace(s)
}

type fileDepPair struct{ from, to string }

// fileDependencyMermaidFromEdges builds one Mermaid flowchart block from raw edge rows.
// Paths are canonicalized so the same file is not duplicated; node labels use the repo-relative path
// (not only the basename) so distinct files like a/ui/lifecycles.ts and b/lifecycles.ts stay distinct.
func fileDependencyMermaidFromEdges(edgeFiles []*metadata.EdgeFile) (diagram string, ok bool) {
	seen := make(map[fileDepPair]bool)
	files := make(map[string]bool)
	for _, e := range edgeFiles {
		from := canonicalOverviewRepoPath(e.CallerFile)
		to := canonicalOverviewRepoPath(e.CalleeFile)
		if from == "" || to == "" || from == to {
			continue
		}
		if isOverviewIgnoredPath(from) || isOverviewIgnoredPath(to) {
			continue
		}
		seen[fileDepPair{from, to}] = true
		files[from] = true
		files[to] = true
	}
	if len(seen) == 0 {
		return "", false
	}
	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	sort.Strings(fileList)
	fileToID := make(map[string]string, len(fileList))
	for i, f := range fileList {
		fileToID[f] = fmt.Sprintf("f%d", i)
	}
	pairList := make([]fileDepPair, 0, len(seen))
	for p := range seen {
		pairList = append(pairList, p)
	}
	sort.Slice(pairList, func(i, j int) bool {
		if pairList[i].from != pairList[j].from {
			return pairList[i].from < pairList[j].from
		}
		return pairList[i].to < pairList[j].to
	})
	direction := "LR"
	if len(files) > 10 {
		direction = "TB"
	}
	var b strings.Builder
	b.WriteString("```mermaid\n")
	b.WriteString("%%{init: {'themeVariables': {'fontSize': '20px'}, 'flowchart': {'nodeSpacing': 80, 'rankSpacing': 60}}}%%\n")
	b.WriteString(fmt.Sprintf("graph %s\n", direction))
	for _, p := range pairList {
		// Unlabeled edges: pairs aggregate CALLS, IMPORTS, EXTENDS, etc.; one arc per file pair.
		b.WriteString(fmt.Sprintf("    %s --> %s\n", fileToID[p.from], fileToID[p.to]))
	}
	for _, f := range fileList {
		b.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", fileToID[f], mermaidFileDepNodeLabel(f)))
	}
	b.WriteString("```\n\n")
	return b.String(), true
}

// emptyFileDependencyGraphExplanation is shown when the index has no cross-file edge pairs for the diagram.
// workflowLang is the dominant language for this overview (e.g. config workflow / indexer focus), not the edge query filter.
func emptyFileDependencyGraphExplanation(workflowLang string) string {
	l := strings.ToLower(strings.TrimSpace(workflowLang))
	base := "No cross-file dependency edges were found in the index. " +
		"This section only lists **distinct files** linked by edges such as resolved **calls** or **extends**/**implements** between indexed symbols; same-file edges are omitted. " +
		"An empty graph is common when callees are unresolved, calls stay within one file, or dependencies point only at external or standard-library code."
	switch l {
	case "java":
		return base + " For Java **call** edges, use the **advanced** indexer (`indexer.type: advanced` + `indexer.advanced_jar_path`); cross-file arcs still require symbols to resolve."
	case "javascript", "typescript", "js", "ts":
		return base + " For JavaScript/TypeScript, in-repo arcs come from resolved **imports** and **calls** to symbols in indexed project files; unresolved specifiers and third-party modules usually do not add file pairs here."
	case "csharp", "cs":
		return base + " For C#, cross-file arcs come from the Roslyn indexer when **calls**, **extends**, **implements**, and similar edges resolve to symbols in other indexed project files."
	default:
		return base + " Capabilities depend on language: Java **call** edges need the advanced Java indexer; JavaScript/TypeScript edges come from the JS/TS indexer when imports and callees resolve to indexed files."
	}
}

// buildFileDependencySectionMermaid returns the file dependency section as Mermaid or an explanation when empty.
// Aggregates edges for every indexed language so Java AST edges still appear when the workflow dominant lang is JS/TS.
// workflowLang tailors the empty-state explanation (see emptyFileDependencyGraphExplanation).
func buildFileDependencySectionMermaid(ctx context.Context, meta *metadata.Store, workflowLang string) string {
	edgeFiles, err := meta.ListEdgeFiles(ctx, "")
	if err != nil {
		return "\n\n## File dependency graph (from AST)\n\n*Could not load edges from the index.*\n"
	}
	var b strings.Builder
	b.WriteString("\n\n## File dependency graph (from AST)\n\n")
	block, ok := fileDependencyMermaidFromEdges(edgeFiles)
	if !ok {
		b.WriteString(emptyFileDependencyGraphExplanation(workflowLang))
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString("File-level dependencies derived from symbol edges (calls, imports, inheritance, etc.); one arc per file pair.\n\n")
	b.WriteString(block)
	return b.String()
}

// BuildFileDependencyGraphMermaid returns a Markdown section with a Mermaid flowchart of file-level
// dependencies (from AST edges). Prefer BuildOverviewVisualSections to get the full overview with both sections.
// lang is ignored: edges include every language in the index (same as the overview appendix graph).
func BuildFileDependencyGraphMermaid(ctx context.Context, meta *metadata.Store, lang string) string {
	_ = lang
	if meta == nil {
		return ""
	}
	edgeFiles, err := meta.ListEdgeFiles(ctx, "")
	if err != nil || len(edgeFiles) == 0 {
		return ""
	}
	block, ok := fileDependencyMermaidFromEdges(edgeFiles)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## File dependency graph (from AST)\n\n")
	b.WriteString("File-level dependencies derived from symbol edges (calls, imports, inheritance, etc.); one arc per file pair.\n\n")
	b.WriteString(block)
	return b.String()
}

// defaultOverviewContextMaxRunes is used only by applyOverviewContextSizeLimit when maxRunes is 0 (e.g. unit tests).
const defaultOverviewContextMaxRunes = 200_000

// overviewMetaReader is the subset of metadata.Store used to build overview index text (allows tests to use fakes).
type overviewMetaReader interface {
	ListFiles(ctx context.Context, lang string, isTest *bool) ([]*metadata.File, error)
	ListSymbolsByLang(ctx context.Context, lang string, kind string) ([]*metadata.Symbol, error)
}

// applyOverviewContextSizeLimit truncates s to at most maxRunes runes when maxRunes >= 0.
// maxRunes 0 uses defaultOverviewContextMaxRunes. Negative maxRunes = no truncation.
func applyOverviewContextSizeLimit(s string, maxRunes int) string {
	if maxRunes < 0 {
		return s
	}
	limit := maxRunes
	if limit == 0 {
		limit = defaultOverviewContextMaxRunes
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	trailer := "\n\n... [truncated: overview index snapshot exceeded max length (internal cap; used in tests only)]\n"
	t := []rune(trailer)
	if len(t) >= limit {
		return string(r[:limit])
	}
	head := limit - len(t)
	if head < 1 {
		head = 1
	}
	return string(r[:head]) + string(t)
}

// truncateUTF8ToMaxRunesWithTrailer truncates s to at most maxRunes UTF-8 runes, appending trailer (if non-empty)
// so the model sees an explicit truncation marker. Used for per-slice overview index bodies when a single slice
// still exceeds the configured rune budget.
func truncateUTF8ToMaxRunesWithTrailer(s string, maxRunes int, trailer string) string {
	if maxRunes <= 0 {
		return s
	}
	trailer = strings.TrimSpace(trailer)
	if trailer != "" {
		trailer = "\n\n" + trailer + "\n"
	}
	r := []rune(s)
	t := []rune(trailer)
	if len(r) <= maxRunes {
		return s
	}
	if len(t) >= maxRunes {
		return string(r[:maxRunes])
	}
	head := maxRunes - len(t)
	if head < 1 {
		head = 1
	}
	return string(r[:head]) + string(t)
}

// buildOverviewContextForSourceFiles builds the same index snapshot shape as BuildOverviewContext, but only for
// repo-relative paths listed in files (non-test production files as returned by the indexer). Used for Plan B
// batched overview LLM calls.
func buildOverviewContextForSourceFiles(ctx context.Context, meta overviewMetaReader, lang string, files []string) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("overview: Meta required")
	}
	lang = overviewCanonicalLang(lang)
	if lang == "" {
		return "", fmt.Errorf("overview: lang required")
	}
	allow := make(map[string]struct{}, len(files))
	for _, f := range files {
		c := canonicalOverviewRepoPath(f)
		if c != "" {
			allow[c] = struct{}{}
		}
	}
	if len(allow) == 0 {
		return "", fmt.Errorf("overview: empty file slice")
	}
	isTest := false
	allFiles, err := meta.ListFiles(ctx, lang, &isTest)
	if err != nil {
		return "", err
	}
	byModule := make(map[string][]string)
	for _, f := range allFiles {
		if isOverviewIgnoredPath(f.File) {
			continue
		}
		cf := canonicalOverviewRepoPath(f.File)
		if _, ok := allow[cf]; !ok {
			continue
		}
		mod := f.Module
		if mod == "" {
			mod = "(default)"
		}
		byModule[mod] = append(byModule[mod], f.File)
	}
	var modKeys []string
	for k := range byModule {
		modKeys = append(modKeys, k)
	}
	sort.Strings(modKeys)
	var b strings.Builder
	b.WriteString("## Files by module\n\n")
	for _, mod := range modKeys {
		paths := append([]string(nil), byModule[mod]...)
		sort.Strings(paths)
		b.WriteString("### ")
		b.WriteString(mod)
		b.WriteString("\n\n")
		for _, p := range paths {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	classes, err := meta.ListSymbolsByLang(ctx, lang, "class")
	if err != nil {
		return "", err
	}
	b.WriteString("## Classes\n\n")
	for _, c := range classes {
		if isOverviewIgnoredPath(c.File) {
			continue
		}
		if _, ok := allow[canonicalOverviewRepoPath(c.File)]; !ok {
			continue
		}
		b.WriteString("- ")
		b.WriteString(c.FQName)
		b.WriteString(" (")
		b.WriteString(c.File)
		b.WriteString(")\n")
	}
	methods, err := meta.ListSymbolsByLang(ctx, lang, "method")
	if err != nil {
		return "", err
	}
	byFile := make(map[string][]*metadata.Symbol)
	for _, m := range methods {
		if isOverviewIgnoredPath(m.File) {
			continue
		}
		if _, ok := allow[canonicalOverviewRepoPath(m.File)]; !ok {
			continue
		}
		byFile[m.File] = append(byFile[m.File], m)
	}
	b.WriteString("\n## Methods (by file)\n\n")
	for _, c := range classes {
		f := c.File
		if isOverviewIgnoredPath(f) {
			continue
		}
		if _, ok := allow[canonicalOverviewRepoPath(f)]; !ok {
			continue
		}
		if list, ok := byFile[f]; ok {
			b.WriteString("### ")
			b.WriteString(f)
			b.WriteString("\n\n")
			for _, m := range list {
				b.WriteString("- ")
				b.WriteString(m.FQName)
				b.WriteString("\n")
			}
			b.WriteString("\n")
			delete(byFile, f)
		}
	}
	var restFiles []string
	for f := range byFile {
		restFiles = append(restFiles, f)
	}
	sort.Strings(restFiles)
	for _, f := range restFiles {
		list := byFile[f]
		b.WriteString("### ")
		b.WriteString(f)
		b.WriteString("\n\n")
		for _, m := range list {
			b.WriteString("- ")
			b.WriteString(m.FQName)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// BuildOverviewContext builds a text summary of the codebase (files, symbols) for the overview/workflows document.
// Lang filters to one language (e.g. "java"). The full index is included (no rune cap for this helper). The LLM path
// uses GenerateOverviewWithMeta (batched slices; optional per-slice index rune cap) when the generator is *LLMOverviewDocGenerator.
func BuildOverviewContext(ctx context.Context, meta *metadata.Store, lang string) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("overview: Meta required")
	}
	lang = overviewCanonicalLang(lang)
	if lang == "" {
		return "", fmt.Errorf("overview: lang required")
	}
	var b strings.Builder
	isTest := false
	files, err := meta.ListFiles(ctx, lang, &isTest)
	if err != nil {
		return "", err
	}
	// Group files by module (exclude output/irrelevant folders)
	byModule := make(map[string][]string)
	for _, f := range files {
		if isOverviewIgnoredPath(f.File) {
			continue
		}
		mod := f.Module
		if mod == "" {
			mod = "(default)"
		}
		byModule[mod] = append(byModule[mod], f.File)
	}
	b.WriteString("## Files by module\n\n")
	for mod, paths := range byModule {
		b.WriteString("### ")
		b.WriteString(mod)
		b.WriteString("\n\n")
		for _, p := range paths {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	classes, err := meta.ListSymbolsByLang(ctx, lang, "class")
	if err != nil {
		return "", err
	}
	b.WriteString("## Classes\n\n")
	for _, c := range classes {
		if isOverviewIgnoredPath(c.File) {
			continue
		}
		b.WriteString("- ")
		b.WriteString(c.FQName)
		b.WriteString(" (")
		b.WriteString(c.File)
		b.WriteString(")\n")
	}
	methods, err := meta.ListSymbolsByLang(ctx, lang, "method")
	if err != nil {
		return "", err
	}
	// Group methods by file for readability (exclude output/irrelevant paths)
	byFile := make(map[string][]*metadata.Symbol)
	for _, m := range methods {
		if isOverviewIgnoredPath(m.File) {
			continue
		}
		byFile[m.File] = append(byFile[m.File], m)
	}
	b.WriteString("\n## Methods (by file)\n\n")
	for _, c := range classes {
		f := c.File
		if isOverviewIgnoredPath(f) {
			continue
		}
		if list, ok := byFile[f]; ok {
			b.WriteString("### ")
			b.WriteString(f)
			b.WriteString("\n\n")
			for _, m := range list {
				b.WriteString("- ")
				b.WriteString(m.FQName)
				b.WriteString("\n")
			}
			b.WriteString("\n")
			delete(byFile, f)
		}
	}
	for f, list := range byFile {
		b.WriteString("### ")
		b.WriteString(f)
		b.WriteString("\n\n")
		for _, m := range list {
			b.WriteString("- ")
			b.WriteString(m.FQName)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// DefaultOverviewPath is the default path for the generated overview document (configurable; this is the fallback).
const DefaultOverviewPath = "docs/documentation.md"

// OverviewVisualAppendixMarker begins the auto-generated Mermaid appendix. SplitOverviewNarrativeAndVisuals uses it
// to separate stable narrative from sections refreshed each run.
const OverviewVisualAppendixMarker = "<!-- asqs:visual-appendix -->"

const defaultOverviewPrompt = `You are an expert technical writer. Write a single Markdown document that serves as a high-level overview of the codebase.
Describe: (1) the main modules and their purpose, (2) the main workflows and how components interact, (3) key dependencies between classes/modules.
Use the following symbol and file listing as the only source of truth. Do not invent files, modules, or APIs that are not listed. Output only the Markdown document, no preamble.

Voice: the document must read as **one authoritative overview** for the repository. Do not hint at how the text was produced (no references to slices, portions, batches, phases, “this section”, “the files listed above”, “the excerpt”, or similar). Do not hedge: avoid words and phrases such as likely, probably, perhaps, maybe, seems, appears to, could, might, or “may” when it signals speculation—state only what the index directly supports in plain, confident language.`

// overviewOutputContractFull is appended to the user message for full overview generation.
const overviewOutputContractFull = `## OUTPUT CONTRACT (mandatory — read last)

Your reply must be **only** the Markdown body of **one** repository overview document. Start with a top-level heading (Markdown #). **No** preamble (“Here is…”). **Do not** wrap the **entire** document in a Markdown code fence. **Do not** include Mermaid blocks or sections titled **Module and file structure** or **File dependency graph** — those are maintained automatically. **Do not** output JSON.

The reader must not see any hint of multi-step or batched generation. Do not use hedging or speculation (likely, probably, perhaps, maybe, seems, appears to, could, might); describe only facts grounded in the index you were given.`

// overviewOutputContractDelta is appended for incremental overview updates (NO_UPDATES or dated delta).
const overviewOutputContractDelta = `## OUTPUT CONTRACT (mandatory — read last)

Reply with **exactly** the single line NO_UPDATES **or** Markdown beginning with "## Overview updates (YYYY-MM-DD)" as required by the system message. **No** other leading or trailing text. **Do not** wrap the entire reply in Markdown code fences. **Do not** include Mermaid or sections titled **Module and file structure** or **File dependency graph**. **Do not** output a full document rewrite.

Each bullet must be factual and tied to the index snapshot; do not hedge (likely, probably, perhaps, maybe, seems, appears to, could, might) and do not describe how the snapshot was produced (no slices, batches, or “this portion” language).`

func appendOverviewUserOutputContract(userBody string, deltaMode bool) string {
	userBody = strings.TrimRight(userBody, "\n\t ")
	block := overviewOutputContractFull
	if deltaMode {
		block = overviewOutputContractDelta
	}
	if userBody == "" {
		return block
	}
	return userBody + "\n\n" + block
}

const defaultOverviewDeltaPrompt = `You maintain a technical overview document for a repository. The EXISTING DOCUMENT is the canonical narrative: do not repeat, rephrase, or summarize it.

You are given indexed symbols and paths (files, classes, methods) that are the only source of truth for what exists in the codebase.

Your job: decide whether that index material contains material facts (new or removed modules, important new files, new or removed public classes/methods, or a clearly changed structure) that are not already reflected in the existing document.

- If there is nothing material to add, output exactly this single line with no other text: NO_UPDATES
- If there are updates: output Markdown only (no preamble), starting with exactly this heading line:
  ## Overview updates (YYYY-MM-DD)
  where YYYY-MM-DD is the date given in the user message (UTC). Then add concise bullet points for new information only. Do not restate what the existing document already covers.
- Never invent features or paths; every bullet must be justified by the index material.
- Write in direct, factual language: do not hedge (likely, probably, perhaps, maybe, seems, appears to, could, might) and do not mention slices, batches, snapshots, or how the index was delivered.
- Do not include Mermaid blocks or sections titled "Module and file structure" or "File dependency graph"; those are maintained automatically elsewhere.`

// LLMOverviewDocGenerator generates the overview document using a ChatCompleter.
type LLMOverviewDocGenerator struct {
	LLM         model.ChatCompleter
	Prompt      string // optional; default describes workflows and dependencies (initial full document)
	DeltaPrompt string // optional; default instructs append-only updates when the overview file already exists
	Path        string // output path for the overview doc (e.g. docs/documentation.md); empty = DefaultOverviewPath
	// FullRewrite when true, always regenerates the full narrative (ignores existing file). When false, an existing file gets append-only LLM deltas plus a refreshed appendix.
	FullRewrite bool
	// MaxCompletionTokensFull when > 0, overrides default 8192 max completion tokens for a full (non-delta) overview LLM call.
	MaxCompletionTokensFull int
}

// SplitOverviewNarrativeAndVisuals separates the narrative body from the auto-generated Mermaid appendix.
// It prefers OverviewVisualAppendixMarker; otherwise it falls back to the first "## Module and file structure" or
// "## File dependency graph (from AST)" heading (legacy documents written before the marker existed).
func SplitOverviewNarrativeAndVisuals(content string) (narrative string, hadVisual bool) {
	content = strings.TrimRight(content, "\n\t ")
	if idx := strings.Index(content, OverviewVisualAppendixMarker); idx >= 0 {
		return strings.TrimSpace(content[:idx]), true
	}
	// Legacy: appendix was appended without a marker.
	candidates := []string{
		"\n\n## Module and file structure\n\n",
		"\n## Module and file structure\n\n",
		"\n\n## Module and file structure\n",
		"\n\n## File dependency graph (from AST)\n\n",
		"\n## File dependency graph (from AST)\n\n",
		"\n\n## File dependency graph (from AST)\n",
	}
	best := -1
	for _, c := range candidates {
		if j := strings.Index(content, c); j >= 0 && (best < 0 || j < best) {
			best = j
		}
	}
	if best < 0 {
		return strings.TrimSpace(content), false
	}
	return strings.TrimSpace(content[:best]), true
}

// GenerateOverview implements OverviewDocGenerator.
func (g *LLMOverviewDocGenerator) GenerateOverview(ctx context.Context, overviewContext string, opts OverviewGenerateOpts) (content string, path string, err error) {
	if g.LLM == nil {
		return "", "", fmt.Errorf("llm overview generator: ChatCompleter required")
	}
	outPath := g.Path
	if outPath == "" {
		outPath = DefaultOverviewPath
	}
	ctxRunes := utf8.RuneCountInString(overviewContext)
	fmt.Fprintf(os.Stderr, "[asqs-overview] llm entry out_path=%s context_bytes=%d context_runes=%d full_rewrite=%v repo_root_set=%v\n",
		outPath, len(overviewContext), ctxRunes, g.FullRewrite, strings.TrimSpace(opts.RepoRoot) != "")
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	incremental := !g.FullRewrite && repoRoot != "" && strings.TrimSpace(overviewContext) != ""
	var existingOnDisk []byte
	if incremental {
		full := filepath.Join(repoRoot, filepath.FromSlash(outPath))
		if data, readErr := os.ReadFile(full); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
			existingOnDisk = data
		}
	}
	if incremental && len(existingOnDisk) > 0 {
		narrative, _ := SplitOverviewNarrativeAndVisuals(string(existingOnDisk))
		if strings.TrimSpace(narrative) != "" {
			fmt.Fprintf(os.Stderr, "[asqs-overview] llm mode=incremental_delta max_completion_tokens=2048 existing_narrative_bytes=%d\n", len(narrative))
			system := strings.TrimSpace(g.DeltaPrompt)
			if system == "" {
				system = defaultOverviewDeltaPrompt
			}
			today := time.Now().UTC().Format("2006-01-02")
			user := appendOverviewUserOutputContract("Today's date (UTC): "+today+"\n\nEXISTING DOCUMENT:\n\n"+narrative+"\n\n---\n\n## Indexed symbols and paths\n\n"+overviewContext, true)
			messages := []model.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: user},
			}
			result, err := overviewLLMCompleteWithRetry(ctx, g.LLM, "incremental_delta", messages, model.CompleteOptions{MaxTokens: 2048})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[asqs-overview] llm incremental_delta Complete failed: %v\n", err)
				return "", "", err
			}
			rawDelta := strings.TrimSpace(result.Content)
			delta := strings.TrimSpace(extractCodeBlockContent(rawDelta))
			// extractCodeBlockContent can return "" when the first ```…``` pair is empty or when there is no closing fence;
			// keep the raw assistant text so we do not drop a valid delta.
			if delta == "" && rawDelta != "" {
				delta = rawDelta
			}
			if delta == "" || strings.EqualFold(delta, "NO_UPDATES") {
				reason := "empty_delta_after_extract"
				if strings.EqualFold(delta, "NO_UPDATES") {
					reason = "NO_UPDATES"
				}
				fmt.Fprintf(os.Stderr, "[asqs-overview] llm incremental_delta done append=false reason=%s narrative_bytes=%d\n", reason, len(narrative))
				return narrative, outPath, nil
			}
			out := narrative + "\n\n" + delta
			fmt.Fprintf(os.Stderr, "[asqs-overview] llm incremental_delta done append=true merged_bytes=%d merged_runes=%d\n", len(out), utf8.RuneCountInString(out))
			return out, outPath, nil
		}
		fmt.Fprintln(os.Stderr, "[asqs-overview] llm incremental skipped: existing file had no narrative body after split; falling back to full_narrative")
	} else if incremental && len(existingOnDisk) == 0 {
		fmt.Fprintln(os.Stderr, "[asqs-overview] llm incremental skipped: no readable non-empty existing overview on disk; using full_narrative")
	} else if !incremental {
		fmt.Fprintln(os.Stderr, "[asqs-overview] llm using full_narrative (full_rewrite or empty repo_root or empty context)")
	}
	// First run, full rewrite mode, or unreadable/empty existing file: generate full narrative.
	system := g.Prompt
	if system == "" {
		system = defaultOverviewPrompt
	}
	user := appendOverviewUserOutputContract(overviewContext, false)
	messages := []model.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	maxTok := 8192
	if g.MaxCompletionTokensFull > 0 {
		maxTok = g.MaxCompletionTokensFull
	}
	fmt.Fprintf(os.Stderr, "[asqs-overview] llm mode=full_narrative max_completion_tokens=%d user_message_bytes=%d\n", maxTok, len(user))
	result, err := overviewLLMCompleteWithRetry(ctx, g.LLM, "full_narrative", messages, model.CompleteOptions{MaxTokens: maxTok})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[asqs-overview] llm full_narrative Complete failed: %v\n", err)
		return "", "", err
	}
	content = strings.TrimSpace(result.Content)
	// Do not use extractCodeBlockContent for the full overview: it slices the first ```…``` region only. Overview
	// documents are Markdown with headings and often include fenced code examples; the first fence can be empty
	// (e.g. ```markdown\n\n```) or a small snippet, which yields content_bytes=0 or a truncated useless body.
	if content == "" {
		fmt.Fprintf(os.Stderr, "[asqs-overview] llm full_narrative warning: empty assistant message (refusal, content filter, or model returned no text)\n")
	}
	fmt.Fprintf(os.Stderr, "[asqs-overview] llm full_narrative done content_bytes=%d content_runes=%d\n", len(content), utf8.RuneCountInString(content))
	return content, outPath, nil
}
