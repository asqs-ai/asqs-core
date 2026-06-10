package retrieval

import (
	"context"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// RetrievalProfile selects graph expansion, edge ordering, and similar-chunk strategies.
// Use with ContextRequest.Profile or PlanOptions.RetrievalProfile.
type RetrievalProfile string

const (
	// ProfileJavaUnit is the legacy behavior: outgoing edges only, Java-shaped enclosing symbol, test similarity.
	ProfileJavaUnit RetrievalProfile = "java_unit"
	// ProfileHTTPAPI favors Nest/Spring handler ↔ route, DTOs, guards, pipes; inbound ROUTE_TO_HANDLER; api_contract listing.
	ProfileHTTPAPI RetrievalProfile = "http_api"
	// ProfileE2EPlaywright favors selectors, TARGETS_API_ROUTE, routes, handlers for full-stack UI tests.
	ProfileE2EPlaywright RetrievalProfile = "e2e_playwright"
	// ProfileReactFeature favors RENDERS, hooks, props types, CALLS, IMPORTS (inbound).
	ProfileReactFeature RetrievalProfile = "react_feature"
	// ProfileNestModule favors INJECTS, module wiring, routes, CONTAINS.
	ProfileNestModule RetrievalProfile = "nest_module"
	// ProfileFullStack merges react_feature UI priorities with http_api backend/route priorities
	// (monorepos: React SPA + Nest/API or Astro TS server in one E2E plan). Does not disable PAGE_ROUTE
	// E2E gap listing on JS/TS (only explicit java_unit does).
	ProfileFullStack RetrievalProfile = "full_stack"
)

// NormalizeRetrievalProfile maps aliases and defaults empty to java_unit.
func NormalizeRetrievalProfile(p RetrievalProfile) RetrievalProfile {
	s := strings.ToLower(strings.TrimSpace(string(p)))
	switch s {
	case "", "java", "java_unit", "java-unit", "unit":
		return ProfileJavaUnit
	case "http_api", "http-api", "api", "backend", "nest_api", "spring":
		return ProfileHTTPAPI
	case "e2e_playwright", "e2e-playwright", "e2e", "playwright", "ui_test":
		return ProfileE2EPlaywright
	case "react_feature", "react-feature", "react", "frontend":
		return ProfileReactFeature
	case "nest_module", "nest-module", "nest", "wiring":
		return ProfileNestModule
	case "full_stack", "full-stack", "fullstack", "react_http_api", "react-http-api", "ui_and_api":
		return ProfileFullStack
	default:
		return ProfileJavaUnit
	}
}

func profileUsesInboundExpansion(p RetrievalProfile) bool {
	return NormalizeRetrievalProfile(p) != ProfileJavaUnit
}

// dependencyEdgePriorityForProfile returns a sort key (lower = earlier in context). inbound is true when
// the connected symbol was reached via GetEdgesTo (caller side).
func dependencyEdgePriorityForProfile(edgeType string, inbound bool, profile RetrievalProfile) int {
	p := NormalizeRetrievalProfile(profile)
	if p == ProfileJavaUnit {
		return dependencyEdgePriority(edgeType)
	}
	u := strings.ToUpper(strings.TrimSpace(edgeType))
	const defaultPri = 100
	switch p {
	case ProfileHTTPAPI:
		switch u {
		case "HANDLER_USES_DTO", "USES_GUARD", "USES_PIPE", "USES_INTERCEPTOR":
			return 10
		case "INJECTS", "INJECTS_NAMED", "IMPLEMENTS_SERVICE", "REGISTERS_SERVICE":
			return 12
		case "ROUTE_TO_HANDLER":
			if inbound {
				return 5
			}
			return 30
		case "CALLS":
			return 20
		case "TARGETS_API_ROUTE":
			return 35
		case "CONTAINS", "EXTENDS", "IMPLEMENTS":
			return 40
		case "IMPORTS":
			return 45
		default:
			return defaultPri
		}
	case ProfileE2EPlaywright:
		switch u {
		case "USES_SELECTOR", "TARGETS_API_ROUTE":
			return 10
		case "ROUTE_TO_HANDLER":
			if inbound {
				return 12
			}
			return 25
		case "CALLS":
			return 20
		case "CONTAINS":
			return 40
		default:
			return defaultPri
		}
	case ProfileReactFeature:
		switch u {
		case "RENDERS", "USES_HOOK", "ACCEPTS_PROPS_TYPE":
			return 10
		case "CALLS":
			return 15
		case "IMPORTS":
			if inbound {
				return 18
			}
			return 50
		case "CONTAINS":
			return 35
		default:
			return defaultPri
		}
	case ProfileNestModule:
		switch u {
		case "INJECTS", "INJECTS_NAMED":
			return 10
		case "IMPLEMENTS_SERVICE", "REGISTERS_SERVICE":
			return 11
		case "MODULE_IMPORTS", "MODULE_EXPORTS", "MODULE_PROVIDERS":
			return 12
		case "ROUTE_TO_HANDLER":
			if inbound {
				return 8
			}
			return 22
		case "CONTAINS":
			return 20
		case "CALLS":
			return 30
		default:
			return defaultPri
		}
	case ProfileFullStack:
		// Merge http_api (routes, DTOs, guards) + react_feature (RENDERS, hooks, props) + light e2e_playwright (selectors).
		switch u {
		case "ROUTE_TO_HANDLER":
			if inbound {
				return 5
			}
			return 28
		case "USES_SELECTOR":
			return 9
		case "HANDLER_USES_DTO", "USES_GUARD", "USES_PIPE", "USES_INTERCEPTOR":
			return 10
		case "INJECTS", "INJECTS_NAMED", "IMPLEMENTS_SERVICE", "REGISTERS_SERVICE":
			return 12
		case "RENDERS", "USES_HOOK", "ACCEPTS_PROPS_TYPE":
			return 10
		case "CALLS":
			return 15
		case "IMPORTS":
			if inbound {
				return 18
			}
			return 45
		case "TARGETS_API_ROUTE":
			return 32
		case "CONTAINS", "EXTENDS", "IMPLEMENTS":
			return 35
		default:
			return defaultPri
		}
	default:
		return dependencyEdgePriority(edgeType)
	}
}

func similarChunkTypesForProfile(p RetrievalProfile) []string {
	switch NormalizeRetrievalProfile(p) {
	case ProfileHTTPAPI:
		return []string{"test", "route", "api_contract"}
	case ProfileE2EPlaywright:
		return []string{"test", "e2e_pattern", "page", "route"}
	case ProfileReactFeature:
		return []string{"test", "definition"}
	case ProfileNestModule:
		return []string{"test", "route", "definition"}
	case ProfileFullStack:
		// React (definition) + API (route, api_contract) + browser E2E (e2e_pattern, page) for typical monorepos.
		return []string{"test", "definition", "route", "api_contract", "e2e_pattern", "page"}
	default:
		return []string{"test"}
	}
}

func shouldAttachAPIContractChunks(p RetrievalProfile) bool {
	switch NormalizeRetrievalProfile(p) {
	case ProfileHTTPAPI, ProfileFullStack:
		return true
	default:
		return false
	}
}

// profileLoadsContainerSiblings is true when we list extra chunks by chunks.parent_symbol_id = enclosing container.
func profileLoadsContainerSiblings(p RetrievalProfile) bool {
	switch NormalizeRetrievalProfile(p) {
	case ProfileHTTPAPI, ProfileNestModule, ProfileFullStack:
		return true
	default:
		return false
	}
}

func isEnclosingContainerKind(kind string) bool {
	k := strings.TrimSpace(kind)
	if k == "" {
		return false
	}
	kl := strings.ToLower(k)
	switch kl {
	case "class", "interface", "struct", "record":
		return true
	}
	switch k {
	case "REACT_COMPONENT", "ANGULAR_COMPONENT", "ANGULARJS_CONTROLLER",
		"NEST_CONTROLLER", "NEST_MODULE", "NEST_GATEWAY":
		return true
	}
	return false
}

// enclosingContainer returns the innermost symbol in the same file that spatially contains sym
// (class/interface/struct/record or common JS/TS/Nest container kinds).
// If line spans are missing or wrong, falls back to an incoming CONTAINS edge (caller in the same file).
func enclosingContainer(ctx context.Context, meta MetaReader, sym *metadata.Symbol) *metadata.Symbol {
	if sym == nil {
		return nil
	}
	syms, err := meta.ListSymbolsByFile(ctx, sym.File)
	if err != nil || len(syms) == 0 {
		return nil
	}
	var best *metadata.Symbol
	for _, s := range syms {
		if s.ID == sym.ID || !isEnclosingContainerKind(s.Kind) {
			continue
		}
		if s.StartLine <= sym.StartLine && sym.EndLine <= s.EndLine {
			if best == nil || s.StartLine > best.StartLine {
				best = s
			}
		}
	}
	if best != nil {
		return best
	}
	// Graph fallback: CONTAINS edges point parent (caller) → child (callee); callee = sym.
	edges, err := meta.GetEdgesTo(ctx, sym.ID)
	if err != nil || len(edges) == 0 {
		return nil
	}
	var graphBest *metadata.Symbol
	bestLine := -1
	for _, e := range edges {
		if strings.ToUpper(strings.TrimSpace(e.EdgeType)) != "CONTAINS" {
			continue
		}
		caller, _ := meta.GetSymbolByID(ctx, e.CallerSymbolID)
		if caller == nil || !isEnclosingContainerKind(caller.Kind) {
			continue
		}
		if caller.File != sym.File {
			continue
		}
		if graphBest == nil || caller.StartLine > bestLine {
			graphBest = caller
			bestLine = caller.StartLine
		}
	}
	return graphBest
}

type graphEdge struct {
	otherID  string
	edgeType string
	inbound  bool
	depth    int
	path     string
}

type graphWalkNode struct {
	symbolID string
	depth    int
	path     []string
}

func collectGraphEdges(ctx context.Context, meta MetaReader, targetID string, profile RetrievalProfile, maxDepth int) []graphEdge {
	if strings.TrimSpace(targetID) == "" {
		return nil
	}
	if maxDepth <= 0 {
		maxDepth = defaultDependencyMaxDepth
	}
	q := []graphWalkNode{{symbolID: targetID, depth: 0, path: nil}}
	bestDepth := map[string]int{targetID: 0}
	bestByOther := map[string]graphEdge{}

	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if n.depth >= maxDepth {
			continue
		}
		nextDepth := n.depth + 1
		direct := collectDirectGraphEdges(ctx, meta, n.symbolID, profile)
		if len(direct) == 0 {
			continue
		}
		sortGraphEdges(direct, profile)
		for _, ge := range direct {
			if ge.otherID == "" || ge.otherID == targetID {
				continue
			}
			step := strings.ToUpper(strings.TrimSpace(ge.edgeType))
			if step == "" {
				step = "UNKNOWN"
			}
			if ge.inbound {
				step += "←"
			}
			path := append(append([]string(nil), n.path...), step)
			ge.depth = nextDepth
			ge.path = strings.Join(path, " -> ")

			if prev, ok := bestByOther[ge.otherID]; !ok || betterGraphEdge(ge, prev, profile) {
				bestByOther[ge.otherID] = ge
			}
			if prevDepth, ok := bestDepth[ge.otherID]; !ok || nextDepth < prevDepth {
				bestDepth[ge.otherID] = nextDepth
				q = append(q, graphWalkNode{symbolID: ge.otherID, depth: nextDepth, path: path})
			}
		}
	}
	out := make([]graphEdge, 0, len(bestByOther))
	for _, ge := range bestByOther {
		out = append(out, ge)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].depth != out[j].depth {
			return out[i].depth < out[j].depth
		}
		pi := dependencyEdgePriorityForProfile(out[i].edgeType, out[i].inbound, profile)
		pj := dependencyEdgePriorityForProfile(out[j].edgeType, out[j].inbound, profile)
		if pi != pj {
			return pi < pj
		}
		ci := dependencyEdgeConfidence(out[i].edgeType)
		cj := dependencyEdgeConfidence(out[j].edgeType)
		if ci != cj {
			return ci > cj
		}
		if out[i].inbound != out[j].inbound {
			return !out[i].inbound
		}
		return out[i].otherID < out[j].otherID
	})
	return out
}

func collectDirectGraphEdges(ctx context.Context, meta MetaReader, symbolID string, profile RetrievalProfile) []graphEdge {
	var raw []graphEdge
	if out, _ := meta.GetEdgesFrom(ctx, symbolID); out != nil {
		for _, e := range out {
			raw = append(raw, graphEdge{otherID: e.CalleeSymbolID, edgeType: e.EdgeType, inbound: false})
		}
	}
	if profileUsesInboundExpansion(profile) {
		if in, _ := meta.GetEdgesTo(ctx, symbolID); in != nil {
			for _, e := range in {
				raw = append(raw, graphEdge{otherID: e.CallerSymbolID, edgeType: e.EdgeType, inbound: true})
			}
		}
	}
	return raw
}

func betterGraphEdge(a, b graphEdge, profile RetrievalProfile) bool {
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	pa := dependencyEdgePriorityForProfile(a.edgeType, a.inbound, profile)
	pb := dependencyEdgePriorityForProfile(b.edgeType, b.inbound, profile)
	if pa != pb {
		return pa < pb
	}
	ca := dependencyEdgeConfidence(a.edgeType)
	cb := dependencyEdgeConfidence(b.edgeType)
	if ca != cb {
		return ca > cb
	}
	if a.inbound != b.inbound {
		return !a.inbound
	}
	return a.otherID < b.otherID
}

func sortGraphEdges(raw []graphEdge, profile RetrievalProfile) {
	sort.Slice(raw, func(i, j int) bool {
		pi := dependencyEdgePriorityForProfile(raw[i].edgeType, raw[i].inbound, profile)
		pj := dependencyEdgePriorityForProfile(raw[j].edgeType, raw[j].inbound, profile)
		if pi != pj {
			return pi < pj
		}
		// For equally-prioritized edge types, prefer higher-confidence relations first
		// (semantic collaborator links before broad structural/import edges).
		ci := dependencyEdgeConfidence(raw[i].edgeType)
		cj := dependencyEdgeConfidence(raw[j].edgeType)
		if ci != cj {
			return ci > cj
		}
		if raw[i].inbound != raw[j].inbound {
			return !raw[i].inbound
		}
		return raw[i].otherID < raw[j].otherID
	})
}

// dependencyEdgeConfidence returns a rough confidence rank (higher = more reliable signal).
// Semantic, behavior-coupled edges rank above broad structural/import-only edges.
func dependencyEdgeConfidence(edgeType string) int {
	switch strings.ToUpper(strings.TrimSpace(edgeType)) {
	case "CALLS", "INJECTS", "INJECTS_NAMED",
		"ROUTE_TO_HANDLER", "HANDLER_USES_DTO", "USES_GUARD", "USES_PIPE", "USES_INTERCEPTOR",
		"TARGETS_API_ROUTE", "CALLS_API", "USES_SELECTOR",
		"RENDERS", "USES_HOOK", "ACCEPTS_PROPS_TYPE",
		"IMPLEMENTS_SERVICE", "REGISTERS_SERVICE":
		return 3
	case "EXTENDS", "IMPLEMENTS", "CONTAINS", "DECLARES",
		"MODULE_IMPORTS", "MODULE_EXPORTS", "MODULE_PROVIDERS", "MODULE_REGISTERS":
		return 2
	case "IMPORTS", "DEPENDS_ON", "DEPENDS_ON_DEV",
		"PACKAGE_MAIN", "PACKAGE_MODULE", "PACKAGE_EXPORT", "PACKAGE_ENTRY", "PACKAGE_BIN":
		return 1
	default:
		return 2
	}
}

func dedupeGraphEdges(raw []graphEdge) []graphEdge {
	seen := make(map[string]bool)
	var out []graphEdge
	for _, g := range raw {
		if g.otherID == "" || seen[g.otherID] {
			continue
		}
		seen[g.otherID] = true
		out = append(out, g)
	}
	return out
}
