package retrieval

import (
	"context"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// edgesExcludingTypes returns inbound edges omitting given edge_type values (case-insensitive).
// Used so TESTS_SOURCE (test→SUT trace links) does not inflate "central dependency" counts on production symbols.
func edgesExcludingTypes(edges []*metadata.Edge, excludeTypes ...string) []*metadata.Edge {
	if len(edges) == 0 {
		return nil
	}
	skip := make(map[string]bool, len(excludeTypes))
	for _, x := range excludeTypes {
		skip[strings.ToUpper(strings.TrimSpace(x))] = true
	}
	var out []*metadata.Edge
	for _, e := range edges {
		if e == nil {
			continue
		}
		if skip[strings.ToUpper(strings.TrimSpace(e.EdgeType))] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func edgeTypeEqual(e *metadata.Edge, want string) bool {
	if e == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(e.EdgeType), strings.TrimSpace(want))
}

// hasInboundTestsSourceTrace is true when materialized TESTS_SOURCE edges indicate a test-side link to this symbol
// or to its enclosing class (Java-style pkg.Type#method → pkg.Type).
func hasInboundTestsSourceTrace(ctx context.Context, meta GapMetaReader, sym *metadata.Symbol) bool {
	if sym == nil || sym.ID == "" || meta == nil {
		return false
	}
	edgesTo, err := meta.GetEdgesTo(ctx, sym.ID)
	if err != nil {
		return false
	}
	for _, e := range edgesTo {
		if edgeTypeEqual(e, metadata.EdgeTypeTestsSource) {
			return true
		}
	}
	classFQ, ok := classFQFromMethodOrType(sym)
	if !ok {
		return false
	}
	candidates, err := meta.ListSymbolsByFQName(ctx, classFQ)
	if err != nil {
		return false
	}
	for _, c := range candidates {
		if c == nil || c.ID == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(c.Kind), "class") {
			continue
		}
		if c.File != sym.File {
			continue
		}
		edgesClass, err := meta.GetEdgesTo(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, e := range edgesClass {
			if edgeTypeEqual(e, metadata.EdgeTypeTestsSource) {
				return true
			}
		}
	}
	return false
}

func classFQFromMethodOrType(sym *metadata.Symbol) (string, bool) {
	if sym == nil {
		return "", false
	}
	k := strings.ToLower(strings.TrimSpace(sym.Kind))
	if k == "class" || k == "interface" || k == "record" || k == "struct" {
		return sym.FQName, true
	}
	if k == "method" || k == "constructor" {
		idx := strings.Index(sym.FQName, "#")
		if idx <= 0 {
			return "", false
		}
		return sym.FQName[:idx], true
	}
	return "", false
}
