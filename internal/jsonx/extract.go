// Package jsonx extracts JSON objects from model output.
//
// Models wrap and garnish their JSON: prose before it, a ```json fence around it, commentary after
// it, occasionally a second object. Anything that asks a model for JSON without a schema-enforced
// decoder needs a defensive extractor, and there should be one of them rather than one per caller.
package jsonx

import "strings"

// ExtractObject returns the first brace-matched JSON object in s, or "" when there is none.
//
// Brace matching is string-aware: a `{` or `}` inside a quoted string does not change depth, so a
// JSON value containing braces — a code snippet, a regex — does not truncate the object early.
func ExtractObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	return ExtractObjectFrom(s, start)
}

// ExtractObjectFrom returns the brace-matched JSON object in s beginning at start, or "".
func ExtractObjectFrom(s string, start int) string {
	inString := false
	var escape bool
	var quote byte
	depth := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == quote {
				inString = false
			}
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		case '"', '\'':
			inString = true
			quote = c
		}
	}
	return ""
}
