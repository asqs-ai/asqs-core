// Package sqlsplit splits a SQL script into individual statements.
//
// It exists because both stores had the same three-line implementation:
//
//	for _, part := range strings.Split(s, ";") { … }
//
// which splits inside string literals, dollar-quoted bodies and comments. The schema files carried
// warning comments telling future authors not to write a semicolon anywhere one might appear —
// including inside a prose comment, after one such comment took down process startup with a
// "syntax error at end of input" naming the wrong statement.
//
// A rule that says "do not use this character" is a bug with documentation, not a design. Postgres
// function bodies, CHECK constraints with string literals, and DEFAULT expressions all legitimately
// contain semicolons.
package sqlsplit

import "strings"

// Statements splits a SQL script on statement-terminating semicolons, ignoring semicolons inside
// single-quoted strings, dollar-quoted strings, line comments and block comments.
//
// Returned statements are trimmed and non-empty; comments are preserved within a statement, which
// Postgres accepts and which keeps error messages pointing at recognisable text.
func Statements(script string) []string {
	var (
		out   []string
		cur   strings.Builder
		runes = []rune(script)
		n     = len(runes)
		i     int
		flush = func() {
			if t := strings.TrimSpace(cur.String()); t != "" {
				out = append(out, t)
			}
			cur.Reset()
		}
		at = func(k int) rune {
			if k < 0 || k >= n {
				return 0
			}
			return runes[k]
		}
	)

	for i < n {
		c := runes[i]
		switch {
		// Line comment: runs to end of line. A semicolon in here is prose, not a terminator.
		case c == '-' && at(i+1) == '-':
			for i < n && runes[i] != '\n' {
				cur.WriteRune(runes[i])
				i++
			}

		// Block comment. Postgres nests these, so track depth rather than stopping at the first */.
		case c == '/' && at(i+1) == '*':
			depth := 1
			cur.WriteRune(runes[i])
			cur.WriteRune(runes[i+1])
			i += 2
			for i < n && depth > 0 {
				if runes[i] == '/' && at(i+1) == '*' {
					depth++
					cur.WriteRune(runes[i])
					cur.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if runes[i] == '*' && at(i+1) == '/' {
					depth--
					cur.WriteRune(runes[i])
					cur.WriteRune(runes[i+1])
					i += 2
					continue
				}
				cur.WriteRune(runes[i])
				i++
			}

		// Single-quoted string. '' is an escaped quote, not a close followed by an open.
		case c == '\'':
			cur.WriteRune(c)
			i++
			for i < n {
				if runes[i] == '\'' {
					if at(i+1) == '\'' {
						cur.WriteRune(runes[i])
						cur.WriteRune(runes[i+1])
						i += 2
						continue
					}
					cur.WriteRune(runes[i])
					i++
					break
				}
				cur.WriteRune(runes[i])
				i++
			}

		// Double-quoted identifier: "" escapes, same shape as above.
		case c == '"':
			cur.WriteRune(c)
			i++
			for i < n {
				if runes[i] == '"' {
					if at(i+1) == '"' {
						cur.WriteRune(runes[i])
						cur.WriteRune(runes[i+1])
						i += 2
						continue
					}
					cur.WriteRune(runes[i])
					i++
					break
				}
				cur.WriteRune(runes[i])
				i++
			}

		// Dollar-quoted string: $$…$$ or $tag$…$tag$. This is how function bodies are written, and
		// a function body is precisely where semicolons are expected.
		case c == '$':
			if tag, ok := dollarTag(runes, i); ok {
				cur.WriteString(tag)
				i += len([]rune(tag))
				for i < n {
					if runes[i] == '$' {
						if closeTag, ok := dollarTag(runes, i); ok && closeTag == tag {
							cur.WriteString(closeTag)
							i += len([]rune(closeTag))
							break
						}
					}
					cur.WriteRune(runes[i])
					i++
				}
			} else {
				cur.WriteRune(c)
				i++
			}

		case c == ';':
			flush()
			i++

		default:
			cur.WriteRune(c)
			i++
		}
	}
	flush()
	return out
}

// dollarTag reads a dollar-quote delimiter starting at i ($$ or $tag$) and reports whether one is
// there. A bare $ followed by anything else — a positional parameter such as $1 — is not a tag.
func dollarTag(runes []rune, i int) (string, bool) {
	if i >= len(runes) || runes[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < len(runes) {
		r := runes[j]
		if r == '$' {
			return string(runes[i : j+1]), true
		}
		isTagRune := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (j > i+1 && r >= '0' && r <= '9')
		if !isTagRune {
			return "", false
		}
		j++
	}
	return "", false
}
