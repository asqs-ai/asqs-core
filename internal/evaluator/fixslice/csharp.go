package fixslice

import "strings"

// sliceCSharp reuses the shared scanner with a C#-aware language policy. C# is structurally close
// to Java (brace-delimited bodies, class/interface/struct/enum/record/namespace frames) but adds
// namespace-level frames and expression-bodied members (`=> expr;`).
func sliceCSharp(body string) string {
	s := newScanner(body, csharpPolicy{})
	if !s.run() {
		return body
	}
	return s.out.String()
}

type csharpPolicy struct{}

func (csharpPolicy) isClassLikeHeader(trimmed string) bool { return isCSharpClassLikeHeader(trimmed) }
func (csharpPolicy) looksLikeMethod(trimmed string) bool   { return looksLikeCSharpMethodLine(trimmed) }

func isCSharpClassLikeHeader(trimmed string) bool {
	head := trimmed
	if i := strings.Index(head, "{"); i >= 0 {
		head = head[:i]
	}
	head = " " + strings.TrimSpace(head) + " "
	for _, k := range []string{" class ", " interface ", " struct ", " enum ", " record ", " namespace "} {
		if strings.Contains(head, k) {
			return true
		}
	}
	return false
}

func looksLikeCSharpMethodLine(trimmed string) bool {
	// Expression-bodied member: keep the signature up to `=>` and emit elidedBody. We flag it here
	// by treating it as a one-line method; emitMethod strips from the first `{` but there is no
	// `{`, so we need a dedicated path. To keep the scanner simple we return true and accept that
	// such lines are emitted verbatim (they're a single line anyway, cheap and correct).
	if strings.Contains(trimmed, "=>") && strings.HasSuffix(trimmed, ";") && strings.Contains(trimmed, "(") {
		// Let the generic emitter keep it verbatim (no `{`, ends with `;`).
		return false
	}
	return looksLikeJavaMethodLine(trimmed)
}
