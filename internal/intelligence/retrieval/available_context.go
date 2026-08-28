package retrieval

import (
	"fmt"
	"strings"
)

// writeAvailableContext renders the inventory the model fetches from when tools are enabled.
//
// It lists each dependency with the identity a tool call needs — fully-qualified name, edge type,
// depth, location — and no body. The body is what costs 300-1500 tokens; the row costs about 15.
//
// # Accuracy is the whole contract
//
// Listing something the tools cannot then fetch is worse than not listing it: the model spends a
// turn on a lookup that fails and learns to distrust the inventory. Every entry therefore requires a
// non-empty fully-qualified name, because that is exactly what get_symbol resolves on. Entries
// without one are dropped rather than rendered with a placeholder.
func writeAvailableContext(rc *RetrievalContext, opts FormatOptions) string {
	type entry struct {
		fq, edge, loc string
		depth         int
	}
	var entries []entry
	seen := map[string]bool{}
	for _, dep := range rc.Dependencies {
		if dep.Symbol == nil {
			continue
		}
		fq := strings.TrimSpace(dep.Symbol.FQName)
		// No fully-qualified name means get_symbol cannot resolve it. Silently dropping is correct:
		// an unfetchable row is a broken promise.
		if fq == "" || seen[fq] {
			continue
		}
		seen[fq] = true
		edge := strings.TrimSpace(dep.EdgeType)
		if edge == "" {
			edge = "uses"
		}
		entries = append(entries, entry{fq: fq, edge: edge, loc: symbolLoc(dep.Symbol), depth: dep.Depth})
	}
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(opts.SectionPrefix)
	b.WriteString("Available context (use tools to retrieve)\n\n")
	b.WriteString("These symbols are indexed but NOT included below. Call `get_symbol(fq_name)` for any " +
		"you actually need — do not guess a signature you have not read.\n\n")
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("- `%s` — %s, depth %d — %s\n", e.fq, e.edge, e.depth, e.loc))
	}

	// Counts, not bodies: "there are 2 existing tests" is a decision the model can act on, and the
	// tool call to act with is named explicitly so it does not have to infer the argument shape.
	if n := len(rc.SimilarTests); n > 0 && opts.ToolsAvailable {
		target := ""
		if rc.TargetMethod != nil && rc.TargetMethod.Symbol != nil {
			target = strings.TrimSpace(rc.TargetMethod.Symbol.FQName)
		}
		b.WriteString("\n")
		if target != "" {
			b.WriteString(fmt.Sprintf("Existing tests touching this target: %d → `find_tests_for(\"%s\")`\n", n, target))
		} else {
			b.WriteString(fmt.Sprintf("Existing tests touching this target: %d → `find_tests_for(...)`\n", n))
		}
	}
	b.WriteString("\nOther lookups: `expand_symbol` (callers/callees), `search_code` (find a pattern), " +
		"`read_file_range` (a specific file range).\n\n")
	return b.String()
}
