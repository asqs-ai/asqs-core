package indexer

import (
	"context"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// LinkAPIClientRequestsToRoutes inserts TARGETS_API_ROUTE edges after a full index run pass,
// matching API_CLIENT_REQUEST and API_ROUTE symbols by HTTP method + path (fq_name encoding).
// Uses fqNameToID from the current run so ordering of files does not matter; symbols from
// earlier runs are not considered unless their fq_name is still present in fqNameToID (re-index).
func LinkAPIClientRequestsToRoutes(ctx context.Context, meta MetadataWriter, repoID string, fqNameToID map[string]string) int {
	routesByKey := make(map[string][]string)
	for fq := range fqNameToID {
		if !strings.HasPrefix(fq, "API_ROUTE:") {
			continue
		}
		meth, path, ok := parseAPIRouteFQ(fq)
		if !ok {
			continue
		}
		key := routeMatchKey(meth, path)
		routesByKey[key] = append(routesByKey[key], fq)
	}

	stored := 0
	for fq := range fqNameToID {
		if !strings.HasPrefix(fq, "API_CLIENT_REQUEST:") {
			continue
		}
		meth, path, ok := parseAPIClientRequestFQ(fq)
		if !ok {
			continue
		}
		callerID := fqNameToID[fq]
		if callerID == "" {
			continue
		}
		key := routeMatchKey(meth, path)
		for _, routeFQ := range routesByKey[key] {
			if routeFQ == "" {
				continue
			}
			calleeID := fqNameToID[routeFQ]
			if calleeID == "" {
				syms, _ := meta.ListSymbolsByFQName(ctx, repoID, routeFQ)
				if len(syms) > 0 {
					calleeID = syms[0].ID
				}
			}
			if calleeID == "" {
				continue
			}
			if meta.InsertEdge(ctx, &metadata.Edge{
				CallerSymbolID: callerID,
				CalleeSymbolID: calleeID,
				EdgeType:       "TARGETS_API_ROUTE",
				RepoID:         repoID,
			}) == nil {
				stored++
			}
		}
	}
	return stored
}

// routeMatchKey is the one place a client call and a route are compared, and it has to settle the
// spelling because the emitters do not agree on it.
//
// The C# indexer lower-cases the path and collapses every `{…}` segment to `*` — ASP.NET routing is
// case-insensitive, so a client calling /api/basketapi reaches a route declared on
// BasketApiController and comparing the two as written never matches. The JS/TS indexer
// (normalizeHttpPath) and the Java side do neither, and Nest spells a parameter `:id` rather than
// `{id}`. LinkAPIClientRequestsToRoutes iterates every API_CLIENT_REQUEST against every API_ROUTE in
// the repository with no language partition, so in a mono-repo indexed in one pass — the mixed C#
// and TypeScript shape the E2E surface detection now routes to — a TS client never linked to the C#
// controller that serves it, and the route stayed reported as uncovered.
//
// A parameter segment therefore normalises to the same token whatever wrote it, and only a whole
// segment does: `v{version:apiVersion}/orders` is a partial hole, and matching it against a client's
// literal `v1/orders` would need a pattern matcher rather than a key. That one stays unlinked.
//
// Case folding is the one place this is wider than the strictest server. ASP.NET matches routes
// case-insensitively and its `[controller]` token expands to a casing no client literal will match,
// so a C# route cannot be linked at all without folding; Spring, by contrast, is case-sensitive by
// default, so two routes there differing only in case would fold together. A repository holding
// such a pair is the cost of linking any C# route at all, and it is not a shape anyone writes.
func routeMatchKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + ":" + normalizeRouteMatchPath(path)
}

// normalizeRouteMatchPath lower-cases a path and reduces every parameter segment to "*".
//
// Only segments that are ENTIRELY a parameter are collapsed, so a literal segment never matches a
// parameter and two different literals never match each other.
func normalizeRouteMatchPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for i, seg := range segments {
		if seg == "" {
			// Keep the leading empty segment so a rooted path stays rooted; drop the rest, which is
			// a trailing or doubled slash.
			if i == 0 {
				out = append(out, "")
			}
			continue
		}
		if isRouteParameterSegment(seg) {
			out = append(out, "*")
			continue
		}
		out = append(out, strings.ToLower(seg))
	}
	joined := strings.Join(out, "/")
	if joined == "" {
		return "/"
	}
	return joined
}

// isRouteParameterSegment reports whether a whole path segment stands for "any value here", in any
// of the spellings the four emitters use: ASP.NET's `{id}` / `{id:int}` / `{*catchAll}`, Nest's and
// Spring's `:id`, an interpolation hole `${id}` from a client literal, and the `*` the C# indexer
// already reduces its own to.
func isRouteParameterSegment(seg string) bool {
	switch {
	case seg == "*":
		return true
	case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
		return true
	case strings.HasPrefix(seg, "${") && strings.HasSuffix(seg, "}"):
		return true
	case strings.HasPrefix(seg, ":") && len(seg) > 1:
		return true
	}
	return false
}

// API_ROUTE:{METHOD}:{path}@{handlerFq} — path may contain ':' (e.g. URLs); split method at first ':'.
func parseAPIRouteFQ(fq string) (method, apiPath string, ok bool) {
	const p = "API_ROUTE:"
	if !strings.HasPrefix(fq, p) {
		return "", "", false
	}
	rest := fq[len(p):]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return "", "", false
	}
	before := rest[:at]
	colon := strings.Index(before, ":")
	if colon < 0 {
		return "", "", false
	}
	method = before[:colon]
	apiPath = before[colon+1:]
	return method, apiPath, method != "" && apiPath != ""
}

// API_CLIENT_REQUEST:{METHOD}:{path}@{caller}:L{line}
func parseAPIClientRequestFQ(fq string) (method, apiPath string, ok bool) {
	const p = "API_CLIENT_REQUEST:"
	if !strings.HasPrefix(fq, p) {
		return "", "", false
	}
	rest := fq[len(p):]
	at := strings.Index(rest, "@")
	if at < 0 {
		return "", "", false
	}
	before := rest[:at]
	colon := strings.Index(before, ":")
	if colon < 0 {
		return "", "", false
	}
	method = before[:colon]
	apiPath = before[colon+1:]
	return method, apiPath, method != "" && apiPath != ""
}
