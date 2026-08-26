package projectintel

import (
	"context"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// SymbolResolver is an optional capability for looking up symbols by FQ name.
// The production *metadata.Store satisfies this interface.
type SymbolResolver interface {
	ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*metadata.Symbol, error)
}

// typeNameRe extracts capitalized type-name tokens, including dotted FQ names.
var typeNameRe = regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]*(?:\.[A-Za-z0-9]+)*)\b`)

// commonDocNoiseNames are high-frequency capitalized tokens found in docs that are never
// repo-specific domain symbols — skipping them avoids pointless resolution lookups.
var commonDocNoiseNames = map[string]bool{
	"String": true, "Object": true, "Integer": true, "Long": true, "Boolean": true,
	"Double": true, "Float": true, "Number": true, "Byte": true, "Short": true,
	"Character": true, "Void": true, "List": true, "Map": true, "Set": true,
	"Collection": true, "Optional": true, "Stream": true, "Iterable": true,
	"Iterator": true, "Array": true, "ArrayList": true, "HashMap": true, "HashSet": true,
	"Date": true, "LocalDate": true, "LocalDateTime": true, "Instant": true,
	"Duration": true, "BigDecimal": true, "Exception": true, "RuntimeException": true,
	"Throwable": true, "Error": true, "Override": true, "Test": true, "Promise": true,
	"Record": true, "Task": true, "IEnumerable": true, "IList": true, "IDictionary": true,
	"Dictionary": true, "Guid": true, "DateTime": true, "Func": true, "Action": true,
	// markdown/prose noise
	"The": true, "This": true, "When": true, "If": true, "For": true, "Note": true,
	"See": true, "Use": true, "Used": true, "True": true, "False": true, "None": true,
	"API": true, "URL": true, "HTTP": true, "REST": true, "JSON": true, "YAML": true,
	"SQL": true, "ID": true, "UUID": true, "OK": true, "EOF": true,
}

// ExtractSymbolRefs extracts distinct capitalized type-name tokens from doc content
// that look like code symbol names. Returns at most 60 names, deduplicated.
func ExtractSymbolRefs(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range typeNameRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		tok := m[1]
		// For dotted names keep the top-level segment for resolution.
		if i := strings.Index(tok, "."); i >= 0 {
			lead := tok[:i]
			if !commonDocNoiseNames[lead] && !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
			continue
		}
		if commonDocNoiseNames[tok] || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
		if len(out) >= 60 {
			break
		}
	}
	return out
}

// ResolveDocSymbolLinks resolves extracted symbol names to symbol IDs via the optional
// SymbolResolver. Returns nil when resolver is nil. Unknown names are silently dropped.
func ResolveDocSymbolLinks(ctx context.Context, repoID string, refs []string, resolver SymbolResolver) []string {
	ids, _ := ResolveDocSymbolLinksWithNames(ctx, repoID, refs, resolver)
	return ids
}

// ResolveDocSymbolLinksWithNames additionally returns the fully-qualified names the ids resolved
// from.
//
// The FQ names are what the ranking boost matches on: symbols.id is regenerated on every reindex,
// so an id-keyed boost would silently stop working after the next index run — the failure mode
// being a boost that quietly does nothing, which is precisely the class of defect this work exists
// to remove.
func ResolveDocSymbolLinksWithNames(ctx context.Context, repoID string, refs []string, resolver SymbolResolver) (ids []string, fqNames []string) {
	if resolver == nil || len(refs) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool)
	seenFQ := make(map[string]bool)
	for _, ref := range refs {
		syms, err := resolver.ListSymbolsByFQName(ctx, repoID, ref)
		if err != nil || len(syms) == 0 {
			continue
		}
		for _, s := range syms {
			if s == nil || s.ID == "" || seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			ids = append(ids, s.ID)
			if s.FQName != "" && !seenFQ[s.FQName] {
				seenFQ[s.FQName] = true
				fqNames = append(fqNames, s.FQName)
			}
		}
	}
	return ids, fqNames
}
