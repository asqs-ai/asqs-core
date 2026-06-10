package indexer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// LangIndexerJSON is the contract for language helpers (tools/java-indexer, tools/csharp-indexer).
// They read source from stdin or path and output JSON to stdout in this shape.
type LangIndexerJSON struct {
	Path    string       `json:"path"`
	Lang    string       `json:"lang"`
	Module  string       `json:"module"`
	IsTest  bool         `json:"is_test"`
	Symbols []SymbolJSON `json:"symbols"`
	Edges   []EdgeJSON   `json:"edges"`
}

type SymbolJSON struct {
	Kind        string          `json:"kind"`
	FQName      string          `json:"fq_name"`
	StartLine   int             `json:"start_line"`
	EndLine     int             `json:"end_line"`
	StartColumn *int            `json:"start_column,omitempty"`
	EndColumn   *int            `json:"end_column,omitempty"`
	Signature   json.RawMessage `json:"signature,omitempty"`
}

type EdgeJSON struct {
	CallerFQName string          `json:"caller_fq_name"`
	CalleeFQName string          `json:"callee_fq_name"`
	EdgeType     string          `json:"edge_type"`
	Signature    json.RawMessage `json:"signature,omitempty"`
}

// ParsedFileFromJSON converts helper JSON output to ParsedFile (caller sets Source separately).
func ParsedFileFromJSON(data []byte, source string) (*ParsedFile, error) {
	var j LangIndexerJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	p := &ParsedFile{
		Path:   j.Path,
		Lang:   j.Lang,
		Module: j.Module,
		IsTest: j.IsTest,
		Source: source,
	}
	for _, s := range j.Symbols {
		p.Symbols = append(p.Symbols, ParsedSymbol{
			Kind:          s.Kind,
			FQName:        s.FQName,
			StartLine:     s.StartLine,
			EndLine:       s.EndLine,
			StartColumn:   s.StartColumn,
			EndColumn:     s.EndColumn,
			SignatureJSON: s.Signature,
		})
	}
	for _, e := range j.Edges {
		pe := ParsedEdge{
			CallerFQName: e.CallerFQName,
			CalleeFQName: e.CalleeFQName,
			EdgeType:     CanonicalEdgeType(e.EdgeType),
		}
		if len(e.Signature) > 0 {
			pe.SignatureJSON = append([]byte(nil), e.Signature...)
		}
		p.Edges = append(p.Edges, pe)
	}
	return p, nil
}

// CanonicalEdgeType normalizes helper-emitted edge types to the storage/retrieval canonical form.
// Mixed legacy casing from helpers (e.g. "calls", "imports", "contains") is normalized here.
func CanonicalEdgeType(raw string) string {
	et := strings.TrimSpace(raw)
	if et == "" {
		return ""
	}
	switch strings.ToLower(et) {
	case "calls":
		return "CALLS"
	case "imports":
		return "IMPORTS"
	case "contains":
		return "CONTAINS"
	case "extends":
		return "EXTENDS"
	case "implements":
		return "IMPLEMENTS"
	default:
		return strings.ToUpper(et)
	}
}

// StubLangIndexer returns a ParsedFile with no symbols/edges (for tests or when parser is unavailable).
func StubLangIndexer(ctx context.Context, path string, lang string, source []byte) (*ParsedFile, error) {
	return &ParsedFile{
		Path: path, Lang: lang, Source: string(source),
		Symbols: nil, Edges: nil,
	}, nil
}

// normalizedSkipPrefixes returns a lowercased, forward-slash list of path prefixes (no trailing slash). Empty strings are omitted.
func normalizedSkipPrefixes(skipPathPrefixes []string) []string {
	var out []string
	for _, p := range skipPathPrefixes {
		n := strings.ToLower(strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(p)), "/"))
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// pathMatchesSkipPrefix returns true if path (repo-relative, any slashes) or its dot-as-slash form equals or is under a normalized prefix.
// The indexer may emit paths as module ids (e.g. "app.lib.angular.angular.js" instead of "app/lib/angular/angular.js"), so we treat dots as path separators for matching.
func pathMatchesSkipPrefix(path string, normalizedPrefixes []string) bool {
	pathNorm := strings.ToLower(filepath.ToSlash(path))
	pathDotsAsSlash := strings.ReplaceAll(pathNorm, ".", "/") // so "app.lib.foo" matches prefix "app/lib"
	for _, prefix := range normalizedPrefixes {
		if pathNorm == prefix || strings.HasPrefix(pathNorm, prefix+"/") {
			return true
		}
		if pathDotsAsSlash == prefix || strings.HasPrefix(pathDotsAsSlash, prefix+"/") {
			return true
		}
	}
	return false
}

// PathMatchesSkipPrefix reports whether path equals or is under any of the skip path prefixes (repo-relative, e.g. "app/lib").
// Handles both slash paths and dot-separated module ids (e.g. "app.lib.angular.foo"). Use when filtering gaps or symbols by skip_path_prefixes.
func PathMatchesSkipPrefix(path string, skipPathPrefixes []string) bool {
	prefixes := normalizedSkipPrefixes(skipPathPrefixes)
	if len(prefixes) == 0 {
		return false
	}
	return pathMatchesSkipPrefix(path, prefixes)
}

// FilterFileVersionsBySkipPrefixes returns a slice containing only FileVersions whose path is not under any skip prefix.
// Use after ScanRepoForFiles so currentFiles never contains skipped paths even if the scanner missed them.
func FilterFileVersionsBySkipPrefixes(files []FileVersion, skipPathPrefixes []string) []FileVersion {
	prefixes := normalizedSkipPrefixes(skipPathPrefixes)
	if len(prefixes) == 0 {
		return files
	}
	var out []FileVersion
	for _, fv := range files {
		if !pathMatchesSkipPrefix(fv.Path, prefixes) {
			out = append(out, fv)
		}
	}
	return out
}

// FilterParsedMapBySkipPrefixes returns a copy of the map with any path that equals or is under a skip prefix removed.
// skipPathPrefixes are repo-relative path prefixes (e.g. "app/lib"); paths use forward slashes. Used so the advanced Java JAR result respects indexer skip_path_prefixes.
func FilterParsedMapBySkipPrefixes(parsedByPath map[string]*ParsedFile, skipPathPrefixes []string) map[string]*ParsedFile {
	prefixes := normalizedSkipPrefixes(skipPathPrefixes)
	if len(prefixes) == 0 {
		return parsedByPath
	}
	out := make(map[string]*ParsedFile)
	for path, pf := range parsedByPath {
		if !pathMatchesSkipPrefix(path, prefixes) {
			out[path] = pf
		}
	}
	return out
}

// AddJavaParsedMapPathAliases adds normalized and lowercase keys for each map entry when absent, so
// LangIndexerFromMap finds JAR output when scan paths differ in leading slash or letter case.
func AddJavaParsedMapPathAliases(m map[string]*ParsedFile) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for _, k := range keys {
		v := m[k]
		if v == nil {
			continue
		}
		canon := filepath.ToSlash(strings.TrimSpace(k))
		canon = strings.TrimPrefix(canon, "/")
		for _, alt := range []string{canon, strings.ToLower(canon)} {
			if alt == "" {
				continue
			}
			if _, ok := m[alt]; !ok {
				m[alt] = v
			}
		}
	}
}

// LangIndexerFromMap returns a LangIndexer that uses a precomputed map of path → ParsedFile (e.g. from RunJAR or RunJSTSIndexer).
// For each path it looks up the ParsedFile (trying path, path without leading slash, and Clean(path) so scan and indexer path formats match), sets Source from the provided source bytes, and returns it.
// If the path is not in the map, it delegates to StubLangIndexer.
func LangIndexerFromMap(parsedByPath map[string]*ParsedFile) LangIndexer {
	return func(ctx context.Context, path string, lang string, source []byte) (*ParsedFile, error) {
		path = filepath.ToSlash(path)
		tryPaths := []string{path, strings.TrimPrefix(path, "/"), filepath.ToSlash(filepath.Clean(path))}
		for _, k := range tryPaths {
			if pf, ok := parsedByPath[k]; ok {
				out := *pf
				out.Source = string(source)
				return &out, nil
			}
		}
		return StubLangIndexer(ctx, path, lang, source)
	}
}
