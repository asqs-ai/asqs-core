package dotnetproj

import "strings"

// StripCSharpCommentsAndStrings blanks out comments, string literals and character literals in C#
// source, leaving code structure and byte offsets of the surviving code intact enough for substring
// matching. Each removed literal becomes a single `_`, and a removed comment becomes nothing.
//
// It exists because "does this file call AddRazorPages()" and "does this type derive from
// ControllerBase" are questions about CODE. Asking them of raw text makes a `// TODO: … MapGet(…)`
// comment turn a Razor Pages application into a mixed one, and a class library holding
// `const string Controller = @"public class FooController : ControllerBase { }"` into a Web API —
// which then gets told to derive its tests from WebApplicationFactory<Program> in a project that
// has no Program.
//
// Verbatim (@"…"), interpolated ($"…", $@"…") and raw ("""…""") strings are all handled, because a
// C# codebase that holds templates or SQL holds them in exactly those forms.
//
// Not a parser: preprocessor directives are left alone (an `#if false` block still counts as code),
// which is the same bound every other file-level rule in this package accepts.
func StripCSharpCommentsAndStrings(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		c := src[i]

		// Comments.
		if c == '/' && i+1 < n {
			if src[i+1] == '/' {
				j := strings.IndexByte(src[i:], '\n')
				if j < 0 {
					break
				}
				b.WriteByte('\n')
				i += j + 1
				continue
			}
			if src[i+1] == '*' {
				j := strings.Index(src[i+2:], "*/")
				if j < 0 {
					break
				}
				// Keep the newlines so line-based callers stay aligned.
				b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+j+2], "\n")))
				i += 2 + j + 2
				continue
			}
		}

		// Raw string literals: three or more quotes, closed by the same number.
		if c == '"' && strings.HasPrefix(src[i:], `"""`) {
			open := 0
			for i+open < n && src[i+open] == '"' {
				open++
			}
			closer := strings.Repeat(`"`, open)
			if j := strings.Index(src[i+open:], closer); j >= 0 {
				raw := src[i : i+open+j+open]
				b.WriteByte('_')
				b.WriteString(strings.Repeat("\n", strings.Count(raw, "\n")))
				i += open + j + open
				continue
			}
			break
		}

		// Verbatim and interpolated-verbatim: @"…", $@"…", @$"…". A doubled "" is an escaped quote.
		if start, ok := csharpVerbatimStart(src, i); ok {
			j := start
			for j < n {
				if src[j] == '"' {
					if j+1 < n && src[j+1] == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteByte('_')
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:j], "\n")))
			i = j
			continue
		}

		// Regular string and character literals, backslash-escaped.
		if c == '"' || c == '\'' {
			quote := c
			j := i + 1
			for j < n {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == quote || src[j] == '\n' {
					j++
					break
				}
				j++
			}
			b.WriteByte('_')
			i = j
			continue
		}

		b.WriteByte(c)
		i++
	}
	return b.String()
}

// csharpVerbatimStart reports the index just past the opening quote of a verbatim string starting at
// i, if one starts there.
func csharpVerbatimStart(src string, i int) (int, bool) {
	switch {
	case strings.HasPrefix(src[i:], `@"`):
		return i + 2, true
	case strings.HasPrefix(src[i:], `$@"`), strings.HasPrefix(src[i:], `@$"`):
		return i + 3, true
	default:
		return 0, false
	}
}
