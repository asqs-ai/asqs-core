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

// stripBlockComments removes /* … */ comments while leaving string literals alone.
//
// The literal awareness is the point. This used to scan for "/*" unconditionally, so any occurrence
// inside a string or a regex — `"/*"`, `split("/*")`, a URL glob, a JS regex like /\/\*/ — started
// a "comment" that swallowed source up to the next "*/" or, failing that, to the end of the chunk.
// The damage is silent and lands in two places at once: the corrupted text is what gets embedded
// (so retrieval matches against source that does not exist) and what gets shown to the model as
// context.
//
// The scanner tracks the literal forms the indexed languages actually use:
//
//   - "…" and '…' with backslash escapes — Java, C#, JS/TS, and most everything else
//   - `…` template literals — JS/TS, where ${…} may contain nested quotes; a nested "/*" inside an
//     interpolation is still inside the template as far as comment stripping is concerned
//   - @"…" verbatim strings — C#, where backslash is NOT an escape and "" is a literal quote
//
// Line comments are consumed too, because a "/*" inside a `// …` comment must not open a block: the
// old scanner would have paired it with a later "*/" and eaten the code in between.
//
// Not handled, deliberately: Java text blocks ("""…""") and JS regex literals are not tracked as
// their own states. Both are handled conservatively — a text block's content is scanned as ordinary
// quoted strings, and a regex containing "/*" is rare enough that the alternative (parsing regex
// context, which needs the preceding token to disambiguate division) costs more correctness than it
// buys.
func stripBlockComments(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	runes := []rune(s)
	n := len(runes)

	at := func(i int) rune {
		if i < 0 || i >= n {
			return 0
		}
		return runes[i]
	}

	i := 0
	for i < n {
		c := runes[i]

		// C# verbatim string: @"…", where "" is an escaped quote and \ is literal.
		if c == '@' && at(i+1) == '"' {
			out.WriteRune(c)
			out.WriteRune(at(i + 1))
			i += 2
			for i < n {
				if runes[i] == '"' {
					if at(i+1) == '"' { // doubled quote stays inside the literal
						out.WriteRune(runes[i])
						out.WriteRune(runes[i+1])
						i += 2
						continue
					}
					out.WriteRune(runes[i])
					i++
					break
				}
				out.WriteRune(runes[i])
				i++
			}
			continue
		}

		// Ordinary quoted strings and template literals.
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			out.WriteRune(c)
			i++
			for i < n {
				if runes[i] == '\\' && i+1 < n {
					out.WriteRune(runes[i])
					out.WriteRune(runes[i+1])
					i += 2
					continue
				}
				out.WriteRune(runes[i])
				if runes[i] == quote {
					i++
					break
				}
				// A newline ends a single- or double-quoted literal in every language indexed here.
				// Without this an unterminated quote would swallow the rest of the file, turning one
				// malformed line into a whole-chunk loss.
				if runes[i] == '\n' && quote != '`' {
					i++
					break
				}
				i++
			}
			continue
		}

		// Line comment: consumed as-is, so a "/*" inside it cannot open a block.
		if c == '/' && at(i+1) == '/' {
			for i < n && runes[i] != '\n' {
				out.WriteRune(runes[i])
				i++
			}
			continue
		}

		// Block comment: the thing this function exists to remove.
		if c == '/' && at(i+1) == '*' {
			i += 2
			for i < n && !(runes[i] == '*' && at(i+1) == '/') {
				i++
			}
			if i < n {
				i += 2
			}
			continue
		}

		out.WriteRune(c)
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
