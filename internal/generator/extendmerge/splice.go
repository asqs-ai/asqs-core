package extendmerge

import (
	"regexp"
	"strings"
)

// hasJavaPackageDeclaration is true when the first substantive line is a Java `package ...;` declaration.
// Used so C# files (namespace + *Tests class name) are not mistaken for Java JUnit sources.
// It scans character-wise rather than line-wise because the line-wise version could not span a
// multi-line block comment: on `/*` with no closing `*/` on the same line it skipped only that
// line, then treated the next line (` * Copyright …`) as the first substantive token and returned
// false. Every Spring/Apache-licensed source file opens with exactly that header, so this reported
// "not Java" for most real repo files — which sent insertInsideClassBody down its append-at-EOF
// fallback and wrote new test methods *after* the class's closing brace, producing
// `class, interface, enum, or record expected` at every appended line.
func hasJavaPackageDeclaration(s string) bool {
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 >= len(s) {
				return false // unterminated block comment: no declaration can follow
			}
			i += 2
		default:
			// First substantive token. A Java compilation unit may only open with `package`
			// (annotations on a package declaration are rare and still followed by `package`).
			return strings.HasPrefix(s[i:], "package ") || strings.HasPrefix(s[i:], "package\t")
		}
	}
	return false
}

// firstTypeOpenBrace returns the index of `{` that opens the class/struct/record body after the header (handles `<T>`).
// Language-neutral brace scanning: used for both C# and Java primary-type body ranges.
func firstTypeOpenBrace(s string, headerEnd int) int {
	i := headerEnd
	angle := 0
	for i < len(s) {
		// Line comment
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			i += 2
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			return -1
		}
		switch s[i] {
		case '<':
			angle++
			i++
			continue
		case '>':
			if angle > 0 {
				angle--
			}
			i++
			continue
		case '{':
			if angle == 0 {
				return i
			}
			i++
			continue
		case ':':
			if angle == 0 {
				i++
				for i < len(s) && s[i] != '{' {
					i++
				}
				if i < len(s) && s[i] == '{' {
					return i
				}
				continue
			}
			i++
			continue
		default:
			if angle != 0 {
				i++
				continue
			}
			// Skip tokens before `{` (e.g. `: BaseClass`, `where T : class`)
			if strings.HasPrefix(strings.ToLower(s[i:]), "where") {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			i++
		}
	}
	return -1
}

// scanToMatchingCloseBrace returns the index of the `}` that matches the `{` at openBrace, skipping strings and comments.
// Language-neutral: used for both C# and Java.
func scanToMatchingCloseBrace(s string, openBrace int) int {
	if openBrace < 0 || openBrace >= len(s) || s[openBrace] != '{' {
		return -1
	}
	depth := 1
	i := openBrace + 1
	for i < len(s) && depth > 0 {
		if next, skipped := skipNonCode(s, i); skipped {
			i = next
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

// skipNonCode returns the index just past a comment, string, verbatim string, or char literal
// starting at i, and whether one was found. Language-neutral across Java and C#.
//
// Extracted so brace scanning and brace-DEPTH measurement cannot drift apart: a `{` inside a
// string literal must be invisible to both, and having two copies of these rules is how one of
// them ends up wrong.
func skipNonCode(s string, i int) (int, bool) {
	if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
		i += 2
		for i < len(s) && s[i] != '\n' {
			i++
		}
		return i, true
	}
	if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
		i += 2
		for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
			i++
		}
		if i+1 < len(s) {
			i += 2
		}
		return i, true
	}
	// Verbatim @"..."
	if i+1 < len(s) && s[i] == '@' && s[i+1] == '"' {
		i += 2
		for i < len(s) {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
		return i, true
	}
	if s[i] == '"' {
		i++
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i < len(s) {
			i++
		}
		return i, true
	}
	if s[i] == '\'' {
		i++
		for i < len(s) && s[i] != '\'' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i < len(s) {
			i++
		}
		return i, true
	}
	return i, false
}

// insertInsideClassBody merges new test snippets into an existing test file.
// For Java JUnit classes, inserts before the last top-level "}" (class body).
// For C#, inserts before the closing brace of the test class inside a namespace (not at namespace scope).
// For JavaScript/TypeScript (describe/test modules, Strapi lifecycles-style modules, etc.), **append** at EOF:
// using "last }" would splice inside nested objects or the wrong function (e.g. Strapi lifecycles.ts).
func insertInsideClassBody(existing []byte, newContent string) string {
	s := string(existing)
	newContent = strings.TrimSpace(newContent)
	if newContent == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// Java JUnit-style: require a Java package line so C# (namespace + class name *Tests) is never misclassified.
	lower := strings.ToLower(s)
	isJavaJUnitLike := hasJavaPackageDeclaration(s) && strings.Contains(s, "class ") &&
		(strings.Contains(s, "@Test") || strings.Contains(s, "org.junit") ||
			strings.Contains(s, "jupiter.api") || strings.Contains(lower, "public class ") || strings.Contains(lower, "class "))
	if isJavaJUnitLike {
		idx := strings.LastIndex(s, "\n}")
		if idx < 0 {
			idx = strings.LastIndex(s, "}")
		}
		if idx < 0 {
			return strings.TrimRight(s, "\n\r") + "\n\n" + newContent + "\n"
		}
		indent := "  "
		indented := indentLines(newContent, indent)
		return s[:idx] + "\n" + indented + "\n" + s[idx:]
	}
	if merged, ok := jsInsertBeforeDescribeClose(s, newContent); ok {
		return merged
	}
	if idx, ok := csharpInsertIndexBeforeTestClassClose(s); ok {
		lineStart := strings.LastIndex(s[:idx], "\n") + 1
		prefixLen := len(s[lineStart:idx]) - len(strings.TrimLeft(s[lineStart:idx], " \t"))
		if prefixLen < 0 {
			prefixLen = 0
		}
		indent := strings.Repeat(" ", prefixLen+4)
		indented := indentLines(newContent, indent)
		return s[:idx] + "\n" + indented + "\n" + s[idx:]
	}
	return strings.TrimRight(s, "\n\r") + "\n\n" + newContent + "\n"
}

// csharpInsertIndexBeforeTestClassClose returns the byte index of the `}` that closes the primary test class
// (prefer a type whose name contains "Test"), so new methods land inside the class, not between class and namespace.
// Thin wrapper over csharpPrimaryTypeBodyRange (extend_payload.go), which also exposes the opening
// brace so a full compilation unit can be unwrapped to its body.
func csharpInsertIndexBeforeTestClassClose(s string) (int, bool) {
	_, closeIdx, ok := csharpPrimaryTypeBodyRange(s)
	if !ok {
		return 0, false
	}
	return closeIdx, true
}

// csharpClassDeclRE matches a C# type declaration line (attributes should live on their own lines above).
var csharpClassDeclRE = regexp.MustCompile(`(?m)^\s*(?:public|internal|file)\s+(?:static\s+|sealed\s+|partial\s+|abstract\s+)*\s*(?:class|struct|record)\s+(\w+)`)

// braceDepthAt returns the net brace depth at index target, counting from index from.
// Negative when more braces closed than opened, which callers treat as "not at this level".
func braceDepthAt(s string, from, target int) int {
	if from < 0 {
		from = 0
	}
	if target > len(s) {
		target = len(s)
	}
	depth := 0
	for i := from; i < target; {
		if next, skipped := skipNonCode(s, i); skipped {
			i = next
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	return depth
}

func indentLines(block, prefix string) string {
	lines := strings.Split(block, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
