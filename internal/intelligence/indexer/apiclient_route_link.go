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
func LinkAPIClientRequestsToRoutes(ctx context.Context, meta MetadataWriter, fqNameToID map[string]string) int {
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
				syms, _ := meta.ListSymbolsByFQName(ctx, routeFQ)
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
			}) == nil {
				stored++
			}
		}
	}
	return stored
}

func routeMatchKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + ":" + strings.TrimSpace(path)
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
