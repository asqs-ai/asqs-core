package indexer

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

const edgeTypeMetricMaxLen = 64

// edgeTypeMetricsKey normalizes edge type labels for metric map keys (bounded cardinality).
func edgeTypeMetricsKey(edgeType string) string {
	k := strings.ToUpper(strings.TrimSpace(edgeType))
	if k == "" {
		return "UNKNOWN"
	}
	if len(k) > edgeTypeMetricMaxLen {
		return k[:edgeTypeMetricMaxLen]
	}
	return k
}

// moduleFQToLikelySourcePaths maps a MODULE-style fq_name (path segments joined by dots) back to
// plausible repo-relative source paths for disambiguation hints.
func moduleFQToLikelySourcePaths(moduleFQ, lang string) []string {
	m := strings.TrimSpace(moduleFQ)
	if m == "" || m == "root" {
		return nil
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	base := strings.ReplaceAll(m, ".", "/")
	var exts []string
	switch lang {
	case "java":
		exts = []string{".java"}
	case "kotlin":
		exts = []string{".kt", ".kts"}
	case "javascript", "typescript":
		exts = []string{".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs"}
	case "csharp":
		exts = []string{".cs"}
	default:
		exts = []string{".ts", ".tsx", ".js", ".jsx", ".java", ".cs", ".kt"}
	}
	seen := make(map[string]struct{})
	var out []string
	for _, ext := range exts {
		p := filepath.ToSlash(base + ext)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// collectImportTargetPathsFromParsed returns repo-relative paths inferred from IMPORTS edges
// in the parsed file (callee MODULE fq → likely file paths).
func collectImportTargetPathsFromParsed(parsed *ParsedFile) []string {
	if parsed == nil || len(parsed.Edges) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, e := range parsed.Edges {
		if strings.ToUpper(strings.TrimSpace(CanonicalEdgeType(e.EdgeType))) != "IMPORTS" {
			continue
		}
		for _, p := range moduleFQToLikelySourcePaths(e.CalleeFQName, parsed.Lang) {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func hintFileSet(hintFiles []string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, p := range hintFiles {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		set[p] = struct{}{}
	}
	return set
}

// resolveSymbolIDForFQName picks a symbol row when ListSymbolsByFQName returns multiple matches.
// hintFiles should include the current source file and paths inferred from IMPORTS (MODULE) edges.
func resolveSymbolIDForFQName(ctx context.Context, meta MetadataWriter, repoID, fqName string, hintFiles []string, preferLang string) (id string, ambiguous bool) {
	return resolveSymbolIDForFQNameCached(ctx, meta, nil, repoID, fqName, hintFiles, preferLang)
}

// fqNameCache holds symbols pre-fetched for a file in one query, keyed by fq_name.
//
// A nil cache is valid and means "always query" — that is what the uncached entry point above
// passes, so behaviour is identical either way and only the number of round trips differs.
type fqNameCache map[string][]*metadata.Symbol

// prefetchFQNames resolves every name a file might need in ONE query.
//
// Absent names are recorded as an empty slice rather than left missing, so a later lookup knows the
// database has already been asked and does not ask again. Without that, every unindexed import —
// third-party packages, which is most of them — would still cost a round trip each.
func prefetchFQNames(ctx context.Context, meta MetadataWriter, repoID string, names []string) fqNameCache {
	if meta == nil || len(names) == 0 {
		return nil
	}
	found, err := meta.ListSymbolsByFQNames(ctx, repoID, names)
	if err != nil {
		// Fall back to the per-name path rather than failing the run: a resolution that costs
		// round trips is worse than one that costs none, but far better than no index.
		return nil
	}
	cache := make(fqNameCache, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		cache[n] = found[n] // nil when the name is not indexed; the key still marks it as asked
	}
	return cache
}

// lookupFQName returns the symbols for fqName, from the cache when it was pre-fetched and from the
// database otherwise.
func lookupFQName(ctx context.Context, meta MetadataWriter, cache fqNameCache, repoID, fqName string) []*metadata.Symbol {
	if cache != nil {
		if syms, asked := cache[fqName]; asked {
			return syms
		}
	}
	syms, err := meta.ListSymbolsByFQName(ctx, repoID, fqName)
	if err != nil {
		return nil
	}
	if cache != nil {
		cache[fqName] = syms
	}
	return syms
}

// edgeResolutionCandidates lists every fq_name a file's edges could ask the database about.
func edgeResolutionCandidates(parsed *ParsedFile) []string {
	if parsed == nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Edges)*2+len(parsed.Symbols))
	for _, e := range parsed.Edges {
		out = append(out, e.CallerFQName, e.CalleeFQName)
	}
	// Java/C# CONTAINS resolution asks for the declaring type of each method.
	if parsed.Lang == "java" || parsed.Lang == "csharp" {
		for _, sym := range parsed.Symbols {
			k := strings.ToLower(sym.Kind)
			if k != "method" && k != "constructor" {
				continue
			}
			if classFQ, _ := methodFQNameToClassFQName(sym.FQName); classFQ != "" {
				out = append(out, classFQ)
			}
		}
	}
	return out
}

// resolveSymbolIDForFQNameCached is resolveSymbolIDForFQName over a pre-fetched cache. The
// disambiguation below is unchanged and deliberately shared: the point of batching is to move where
// the rows come from, not which row wins.
func resolveSymbolIDForFQNameCached(ctx context.Context, meta MetadataWriter, cache fqNameCache, repoID, fqName string, hintFiles []string, preferLang string) (id string, ambiguous bool) {
	fqName = strings.TrimSpace(fqName)
	if fqName == "" || meta == nil {
		return "", false
	}
	syms := lookupFQName(ctx, meta, cache, repoID, fqName)
	if len(syms) == 0 {
		return "", false
	}
	if len(syms) == 1 {
		return syms[0].ID, false
	}
	hints := hintFileSet(hintFiles)
	if len(hints) == 0 {
		return syms[0].ID, true
	}
	preferLang = strings.ToLower(strings.TrimSpace(preferLang))
	var matched []*metadata.Symbol
	for _, s := range syms {
		f := filepath.ToSlash(strings.TrimSpace(s.File))
		if _, ok := hints[f]; ok {
			if preferLang != "" && !strings.EqualFold(strings.TrimSpace(s.Lang), preferLang) {
				continue
			}
			matched = append(matched, s)
		}
	}
	if len(matched) == 0 {
		for _, s := range syms {
			f := filepath.ToSlash(strings.TrimSpace(s.File))
			if _, ok := hints[f]; ok {
				matched = append(matched, s)
			}
		}
	}
	if len(matched) == 1 {
		return matched[0].ID, false
	}
	if len(matched) > 1 {
		sort.Slice(matched, func(i, j int) bool { return matched[i].File < matched[j].File })
		return matched[0].ID, true
	}
	// No file match: fall back to first row (legacy) but mark ambiguous.
	return syms[0].ID, true
}

// resolveCSharpImportCalleeID maps a using-namespace string to an existing symbol id (TYPE, MODULE, …)
// by trimming suffixes, similar to resolveJavaImportCalleeID.
func resolveCSharpImportCalleeID(ctx context.Context, meta MetadataWriter, cache fqNameCache, repoID string, symbolIDByFQName, fqNameToID map[string]string, raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for cur := s; cur != ""; {
		if id := symbolIDByFQName[cur]; id != "" {
			return id
		}
		if id := fqNameToID[cur]; id != "" {
			return id
		}
		if syms := lookupFQName(ctx, meta, cache, repoID, cur); len(syms) > 0 {
			return syms[0].ID
		}
		i := strings.LastIndex(cur, ".")
		if i <= 0 {
			break
		}
		cur = cur[:i]
	}
	return ""
}
