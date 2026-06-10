package indexer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxSecondaryTemplateBytes = 256_000

// secondaryChunkMaxContentRunes is the approximate token budget (as runes) for one secondary chunk body + header overhead.
func secondaryChunkMaxContentRunes(cfg ChunkConfig) int {
	mt, ct := cfg.MaxTokens, cfg.CharsPerToken
	if mt <= 0 || ct <= 0 {
		d := DefaultChunkConfig()
		if mt <= 0 {
			mt = d.MaxTokens
		}
		if ct <= 0 {
			ct = d.CharsPerToken
		}
	}
	n := mt * ct
	if n < 512 {
		n = 512
	}
	return n
}

func secondaryTemplateReadCapBytes(cfg ChunkConfig) int {
	runes := secondaryChunkMaxContentRunes(cfg)
	approx := runes * 4
	if approx > maxSecondaryTemplateBytes {
		return maxSecondaryTemplateBytes
	}
	if approx < 8192 {
		return 8192
	}
	return approx
}

// secondaryChunkPlans emits optional route manifest and Angular template file chunks (Phase C).
func secondaryChunkPlans(parsed *ParsedFile, repoID, repoRoot string, cfg ChunkConfig, sanitize SanitizeOptions) []ChunkPlan {
	if !cfg.EnableSecondaryChunks || repoRoot == "" {
		return nil
	}
	var out []ChunkPlan
	if p := routeManifestChunkPlan(parsed, repoID, cfg); p != nil {
		out = append(out, *p)
	}
	out = append(out, angularTemplateChunkPlans(parsed, repoID, repoRoot, cfg, sanitize)...)
	return out
}

func routeManifestChunkPlan(parsed *ParsedFile, repoID string, cfg ChunkConfig) *ChunkPlan {
	var routes []string
	for _, s := range parsed.Symbols {
		u := strings.ToUpper(strings.TrimSpace(s.Kind))
		if u == "API_ROUTE" || u == "PAGE_ROUTE" {
			routes = append(routes, s.FQName)
		}
	}
	if len(routes) == 0 {
		return nil
	}
	sort.Strings(routes)
	maxBody := secondaryChunkMaxContentRunes(cfg) - 256 // leave room for header + footer
	if maxBody < 128 {
		maxBody = 128
	}
	body, routesOmitted := buildRouteManifestBody(routes, maxBody)
	meta := map[string]interface{}{
		"chunk_role":     "route_manifest",
		"file":           parsed.Path,
		"route_count":    len(routes),
		"routes_in_body": len(routes) - routesOmitted,
		"routes_omitted": routesOmitted,
	}
	if m := strings.TrimSpace(parsed.Module); m != "" {
		meta["module"] = m
	}
	metaJSON, _ := json.Marshal(meta)
	header := ""
	if cfg.EnrichChunkContent {
		maxH := cfg.MaxChunkHeaderRunes
		if maxH <= 0 {
			maxH = 512
		}
		header = truncateRunes(fmt.Sprintf("[chunk_role=route_manifest file=%s routes=%d]\n\n", parsed.Path, len(routes)), maxH) + "\n\n"
	}
	return &ChunkPlan{
		Content:       header + body,
		File:          parsed.Path,
		Lang:          parsed.Lang,
		ChunkType:     "route",
		StartLine:     1,
		EndLine:       1,
		RepoID:        repoID,
		SymbolFQ:      "",
		SymbolKind:    "ROUTE_MANIFEST",
		ChunkIndex:    0,
		ParentFQ:      "",
		SecondaryRole: "route_manifest",
		MetadataJSON:  metaJSON,
	}
}

func angularTemplateChunkPlans(parsed *ParsedFile, repoID, repoRoot string, cfg ChunkConfig, sanitize SanitizeOptions) []ChunkPlan {
	dir := filepath.Dir(parsed.Path)
	var out []ChunkPlan
	for _, s := range parsed.Symbols {
		if !strings.EqualFold(strings.TrimSpace(s.Kind), "ANGULAR_TEMPLATE") {
			continue
		}
		rel := templateRelativePathFromSignature(s.SignatureJSON)
		if rel == "" {
			continue
		}
		abs := filepath.Clean(filepath.Join(repoRoot, filepath.ToSlash(dir), filepath.ToSlash(rel)))
		if !strings.HasPrefix(abs, filepath.Clean(repoRoot)) {
			continue
		}
		b, err := os.ReadFile(abs)
		if err != nil || len(b) == 0 {
			continue
		}
		capBytes := secondaryTemplateReadCapBytes(cfg)
		if len(b) > capBytes {
			b = b[:capBytes]
		}
		text := string(b)
		text = Sanitize(text, sanitize)
		comp := componentSymFromSignature(s.SignatureJSON)
		meta := map[string]interface{}{
			"chunk_role":    "angular_template_file",
			"template_path": rel,
			"source_file":   parsed.Path,
			"component_sym": comp,
			"symbol_kind":   s.Kind,
			"fq_name":       s.FQName,
		}
		if m := strings.TrimSpace(parsed.Module); m != "" {
			meta["module"] = m
		}
		mergeStructuredSignatureIntoChunkMetadata(meta, s.SignatureJSON)
		metaJSON, _ := json.Marshal(meta)
		header := ""
		if cfg.EnrichChunkContent {
			maxH := cfg.MaxChunkHeaderRunes
			if maxH <= 0 {
				maxH = 512
			}
			header = truncateRunes(fmt.Sprintf("[chunk_role=angular_template_file fq_name=%s template=%s component=%s]\n\n",
				s.FQName, rel, comp), maxH) + "\n\n"
		}
		endLine := strings.Count(text, "\n") + 1
		if endLine < 1 {
			endLine = 1
		}
		out = append(out, ChunkPlan{
			Content:       header + text,
			File:          parsed.Path,
			Lang:          parsed.Lang,
			ChunkType:     "definition",
			StartLine:     1,
			EndLine:       endLine,
			RepoID:        repoID,
			SymbolFQ:      s.FQName,
			SymbolKind:    s.Kind,
			ChunkIndex:    0,
			ParentFQ:      "",
			SecondaryRole: "angular_template_file",
			MetadataJSON:  metaJSON,
		})
	}
	return out
}

func templateRelativePathFromSignature(sig []byte) string {
	if len(sig) == 0 {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(sig, &m) != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p)
	}
	return ""
}

func componentSymFromSignature(sig []byte) string {
	if len(sig) == 0 {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(sig, &m) != nil {
		return ""
	}
	if c, ok := m["component_sym"].(string); ok {
		return strings.TrimSpace(c)
	}
	return ""
}

// buildRouteManifestBody joins route FQ names up to maxRunes (rune count), appending an omission footer when needed.
func buildRouteManifestBody(routes []string, maxRunes int) (body string, omitted int) {
	if len(routes) == 0 {
		return "", 0
	}
	if maxRunes <= 0 {
		return strings.Join(routes, "\n"), 0
	}
	var b strings.Builder
	for i, r := range routes {
		sep := "\n"
		if i == 0 {
			sep = ""
		}
		candidate := b.String() + sep + r
		remaining := len(routes) - i - 1
		footer := ""
		if remaining > 0 {
			footer = fmt.Sprintf("\n\n... (%d more routes omitted)", remaining)
		}
		if utf8.RuneCountInString(candidate+footer) > maxRunes {
			if b.Len() == 0 {
				rn := []rune(r)
				avail := maxRunes - utf8.RuneCountInString(footer) - 3
				if avail < 1 {
					avail = 1
				}
				if len(rn) > avail {
					r = string(rn[:avail]) + "..."
				}
				return r + footer, remaining
			}
			omitted = len(routes) - i
			return b.String() + fmt.Sprintf("\n\n... (%d routes omitted)", omitted), omitted
		}
		if i == 0 {
			b.WriteString(r)
		} else {
			b.WriteString("\n")
			b.WriteString(r)
		}
	}
	return b.String(), 0
}
