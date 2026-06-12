package overview

import "strings"

// extractCodeBlockContent returns the content inside the first Markdown code fence (```), or the
// input unchanged when there is no fence. Mirrors generator.extractCodeBlockContent so the overview
// package stays self-contained.
func extractCodeBlockContent(s string) string {
	s = strings.TrimSpace(s)
	const fence = "```"
	start := strings.Index(s, fence)
	if start < 0 {
		return s
	}
	afterOpen := start + len(fence)
	if afterOpen >= len(s) {
		return s
	}
	rest := s[afterOpen:]
	if i := strings.Index(rest, "\n"); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = strings.TrimSpace(rest)
	}
	end := strings.Index(rest, fence)
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}
