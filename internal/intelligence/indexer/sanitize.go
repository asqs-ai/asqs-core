package indexer

import (
	"regexp"
	"strings"
	"unicode"
)

// SanitizeOptions control how chunk content is cleaned to reduce injection risk.
type SanitizeOptions struct {
	// MaxCommentRunes limits comment/docstring length (0 = no limit). Truncate long docs.
	MaxCommentRunes int
	// StripBlockComments removes block comments (/* ... */, /** ... */).
	StripBlockComments bool
	// NormalizeWhitespace collapses runs of space/newline to single space.
	NormalizeWhitespace bool
	// DisallowPatterns are regexes; matching substrings are removed or replaced.
	DisallowPatterns []*regexp.Regexp
}

// DefaultSanitizeOptions returns conservative defaults for code chunks.
func DefaultSanitizeOptions() SanitizeOptions {
	return SanitizeOptions{
		MaxCommentRunes:     500,
		StripBlockComments:  true,
		NormalizeWhitespace: false, // keep line structure for code
	}
}

// Sanitize cleans content to reduce injection and keep chunks safe for embedding/LLM context.
func Sanitize(content string, opts SanitizeOptions) string {
	s := content
	if opts.StripBlockComments {
		s = stripBlockComments(s)
	}
	if opts.MaxCommentRunes > 0 {
		s = truncateLongComments(s, opts.MaxCommentRunes)
	}
	for _, re := range opts.DisallowPatterns {
		s = re.ReplaceAllString(s, "")
	}
	if opts.NormalizeWhitespace {
		s = normalizeWhitespace(s)
	}
	// Guard DB writes and downstream prompt building from malformed source bytes.
	// Postgres text rejects NUL (0x00), and invalid UTF-8 can appear when files are mis-encoded.
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(s)
}

func stripBlockComments(s string) string {
	// Remove /* ... */ and /** ... */
	var out strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if i+1 < len(runes) && runes[i] == '/' && runes[i+1] == '*' {
			i += 2
			for i+1 < len(runes) && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			if i+1 < len(runes) {
				i += 2
			}
			continue
		}
		out.WriteRune(runes[i])
		i++
	}
	return out.String()
}

func truncateLongComments(s string, maxRunes int) string {
	// Truncate single-line // and multi-line /** ... */ and /// ...
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "///") {
			if len([]rune(line)) > maxRunes {
				lines[i] = string([]rune(line)[:maxRunes]) + "…"
			}
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeWhitespace(s string) string {
	var out strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				out.WriteRune(' ')
				prevSpace = true
			}
		} else {
			out.WriteRune(r)
			prevSpace = false
		}
	}
	return out.String()
}
