package metadata

import "strings"

// BareFQName strips the B25 C# parameterization from a fully-qualified name: the member's
// parameter list ("(int,List<T>)") and every generic marker ("<T,U>"), returning the pre-B25 form
// every name-derivation consumer was written against. Java and JS/TS FQNames contain neither
// construct, so for them this is the identity function — which is exactly the cross-language
// contract: derivation sites call BareFQName unconditionally, lookup sites use the raw name.
//
// This is THE canonical strip. The C# indexer mirrors it when it stores signature_json.
// bare_fq_name, and migration 0008's generated simple_name expression is its SQL twin; a format
// change must update all three together.
func BareFQName(fq string) string {
	fq = strings.TrimSpace(fq)
	if fq == "" {
		return ""
	}
	// Parameter list: everything from the first '(' after the member separator. Nothing legal
	// follows a parameter list, so cutting to end-of-string is exact.
	if hash := strings.IndexByte(fq, '#'); hash >= 0 {
		if paren := strings.IndexByte(fq[hash:], '('); paren >= 0 {
			fq = fq[:hash+paren]
		}
	}
	if !strings.ContainsRune(fq, '<') {
		return fq
	}
	// Generic markers, depth-aware: "Type<Dictionary<K,V>>" drops cleanly even though the C#
	// indexer emits declaration-form parameters ("<T,U>") that never nest.
	var b strings.Builder
	b.Grow(len(fq))
	depth := 0
	for _, r := range fq {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
