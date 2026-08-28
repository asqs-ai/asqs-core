package websearch

import "strings"

// SanitizeQuery enforces the bundle's query-hygiene rule: library and API terms may leave the
// process; the customer's identity and code may not.
//
// The deny-set is DERIVED at wiring time — repo_id segments plus the repository's own root
// packages, computed from its indexed symbols — not hand-maintained, because a hand-maintained
// blocklist is stale the day after it is written. On top of the deny-set:
//
//   - Path-shaped tokens (anything with '/' or '\') are dropped whole. Repository-relative paths
//     are customer data, and no documentation search needs one.
//   - The query is length-capped. A 4000-rune "query" is a paste, not a search.
//
// The returned string is EXACTLY what goes on the wire, and the caller audits it as such — that
// audit line is the compliance evidence for "prove nothing left the boundary that shouldn't".
func SanitizeQuery(query string, denyTokens []string) string {
	const maxQueryRunes = 300
	fields := strings.Fields(query)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.ContainsAny(f, `/\`) {
			continue
		}
		low := strings.ToLower(f)
		denied := false
		for _, d := range denyTokens {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && strings.Contains(low, d) {
				denied = true
				break
			}
		}
		if !denied {
			kept = append(kept, f)
		}
	}
	out := strings.Join(kept, " ")
	if r := []rune(out); len(r) > maxQueryRunes {
		out = string(r[:maxQueryRunes])
	}
	return strings.TrimSpace(out)
}
