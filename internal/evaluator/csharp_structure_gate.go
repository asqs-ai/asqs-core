package evaluator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// csharpHeaderLineRE matches a line that may legally appear between the top of a C# file and its
// first type declaration.
//
// Wider than Java's: C# has file-scoped and block-scoped namespaces, global and aliased usings,
// assembly-level attributes, preprocessor directives, and the braces of a block namespace.
var csharpHeaderLineRE = regexp.MustCompile(`^\s*(?:(?:global\s+)?using\s|namespace\s|extern\s+alias\s|\[|\]|#|\{|\}|$)`)

// csharpStrayTokenBeforeTypeReason reports content between the file header and the first type
// declaration — what roslyn rejects as "A namespace cannot directly contain members such as fields,
// methods or statements".
//
// The C# twin of the Java gate, and it has to be more forgiving in two directions at once. An
// attribute wraps its argument list across lines exactly as a Java annotation does, so the same
// paren-depth continuation applies; and a preprocessor directive, a block namespace's brace and an
// assembly-level attribute are all legal here and none of them is a statement.
//
// Top-level statements are the one C# shape this cannot judge, and they are excluded at the call
// site rather than here: a Program.cs with no type declaration at all is valid, and reCSharpTypeDecl
// finding nothing already returns "".
func csharpStrayTokenBeforeTypeReason(stripped string) string {
	loc := reCSharpTypeDecl.FindStringIndex(stripped)
	if loc == nil {
		return ""
	}
	head := stripped[:loc[0]]
	depth := 0
	for i, line := range strings.Split(head, "\n") {
		open := depth > 0
		depth += strings.Count(line, "(") - strings.Count(line, ")")
		if depth < 0 {
			depth = 0
		}
		if open {
			continue // a continuation of an attribute's argument list
		}
		// A wrapped attribute whose list is still open on `[` rather than `(`.
		if strings.Count(line, "[") > strings.Count(line, "]") {
			continue
		}
		if csharpHeaderLineRE.MatchString(line) {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		return fmt.Sprintf(
			"stray token %q at line %d, before the first type declaration; roslyn rejects this as "+
				"\"a namespace cannot directly contain members such as fields, methods or statements\"",
			truncateForReason(strings.TrimSpace(line)), i+1)
	}
	return ""
}

// csharpKeywords are the words that may legally follow a closing parenthesis.
//
// `where` and `when` are the two that matter most and neither exists in Java: a generic constraint
// follows a parameter list, and an exception filter follows a catch header. Missing either would
// reject ordinary code.
var csharpKeywords = map[string]bool{
	"abstract": true, "as": true, "base": true, "bool": true, "break": true, "byte": true,
	"case": true, "catch": true, "char": true, "checked": true, "class": true, "const": true,
	"continue": true, "decimal": true, "default": true, "delegate": true, "do": true,
	"double": true, "else": true, "enum": true, "event": true, "explicit": true, "extern": true,
	"false": true, "finally": true, "fixed": true, "float": true, "for": true, "foreach": true,
	"goto": true, "if": true, "implicit": true, "in": true, "int": true, "interface": true,
	"internal": true, "is": true, "lock": true, "long": true, "namespace": true, "new": true,
	"null": true, "object": true, "operator": true, "out": true, "override": true, "params": true,
	"private": true, "protected": true, "public": true, "readonly": true, "ref": true,
	"return": true, "sbyte": true, "sealed": true, "short": true, "sizeof": true, "stackalloc": true,
	"static": true, "string": true, "struct": true, "switch": true, "this": true, "throw": true,
	"true": true, "try": true, "typeof": true, "uint": true, "ulong": true, "unchecked": true,
	"unsafe": true, "ushort": true, "using": true, "virtual": true, "void": true, "volatile": true,
	"while": true,
	// Contextual keywords that follow a parenthesis in ordinary code.
	"where": true, "when": true, "with": true, "record": true, "async": true, "await": true,
	"yield": true, "var": true, "nameof": true, "init": true, "get": true, "set": true,
	"global": true, "partial": true, "required": true, "scoped": true, "file": true,
}

// csharpControlKeywords open a header whose closing parenthesis is followed by the statement it
// governs, so an identifier after it is the statement, not a syntax error.
var csharpControlKeywords = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true, "catch": true,
	"using": true, "lock": true, "fixed": true, "when": true,
}

// csharpCastTypeRE matches what may legally sit inside a cast's parentheses.
var csharpCastTypeRE = regexp.MustCompile(`^[\w.]+(?:<[\w\s,.<>\[\]?]*>)?(?:\[\s*\])*\??$`)

// csharpTupleTypeRE matches a parenthesised tuple TYPE — `(int Total, string Label)` — whose
// closing parenthesis is legally followed by the member's name.
//
// C# has no Java equivalent, and without this every method returning a named tuple reads as two
// statements run together. `(1, "a")` is a tuple VALUE and does not match, because its elements are
// not `type name` pairs.
var csharpTupleTypeRE = regexp.MustCompile(`^\s*[\w.<>\[\]?]+\s+\w+\s*(?:,\s*[\w.<>\[\]?]+\s+\w+\s*)+$`)

// CSharpStatementStructureReason reports two statements run together with nothing between them —
// what roslyn rejects as "; expected".
//
// The C# arm of the Java gate. It is bounded harder because C# has many more legal shapes where an
// identifier follows a closing parenthesis: a tuple return type, a generic constraint, an exception
// filter, a control header, a cast. Each of those is a correct file that this must not refuse.
func CSharpStatementStructureReason(content string) string {
	// dotnetproj's stripper knows every C# literal form — verbatim, interpolated, raw — and keeps
	// the newline count, so a reported line number matches the file the author will open.
	stripped := dotnetproj.StripCSharpCommentsAndStrings(content)
	for i := 0; i < len(stripped); i++ {
		if stripped[i] != ')' {
			continue
		}
		j := i + 1
		for j < len(stripped) && (stripped[j] == ' ' || stripped[j] == '\t') {
			j++
		}
		if j >= len(stripped) || !isIdentStart(stripped[j]) {
			continue
		}
		end := j
		for end < len(stripped) && isIdentPart(stripped[end]) {
			end++
		}
		if csharpKeywords[stripped[j:end]] {
			continue
		}
		open, ok := matchingOpenParen(stripped, i)
		if !ok {
			continue
		}
		if csharpSkipParenGroup(stripped, open, i) {
			continue
		}
		return fmt.Sprintf(
			"malformed statement at line %d: %q follows a closing parenthesis with no operator, "+
				"separator or keyword between them; roslyn rejects this as \"; expected\"",
			1+strings.Count(stripped[:i], "\n"), truncateForReason(strings.TrimSpace(lineAround(stripped, i))))
	}
	return ""
}

// csharpSkipParenGroup reports whether the group is one of the legal shapes whose closing
// parenthesis may be followed by an identifier.
func csharpSkipParenGroup(s string, open, closeIdx int) bool {
	inner := s[open+1 : closeIdx]
	// A tuple TYPE: `(int Total, string Label) Describe()`.
	if csharpTupleTypeRE.MatchString(inner) {
		return true
	}
	k := open - 1
	for k >= 0 && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
		k--
	}
	if k >= 0 && isIdentPart(s[k]) {
		start := k
		for start >= 0 && isIdentPart(s[start]) {
			start--
		}
		if csharpControlKeywords[s[start+1:k+1]] {
			return true
		}
		// An attribute's argument list, possibly qualified: `[Trait.Kind("x")]`. Walk back over
		// dotted segments to the `[`.
		for start >= 0 && s[start] == '.' {
			start--
			for start >= 0 && isIdentPart(s[start]) {
				start--
			}
		}
		for start >= 0 && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
			start--
		}
		if start >= 0 && s[start] == '[' {
			return true
		}
		// A plain call or a parameter list. Not skipped.
		return false
	}
	// Nothing but an operator or a separator before the group: a cast or a grouping expression.
	// Only a cast may be followed by an identifier.
	return csharpCastTypeRE.MatchString(strings.TrimSpace(inner))
}

// The small scanning helpers below are shared with the Java statement-structure gate in asqs-go.
// That gate is not ported here, so they travel with the C# arm that needs them rather than being
// left as a dangling dependency on a file this repository does not have.

// matchingOpenParen scans back from a closing paren for its partner.
func matchingOpenParen(s string, close int) (int, bool) {
	depth := 0
	for i := close; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// lineAround returns the line containing index i.
func lineAround(s string, i int) string {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	end := strings.IndexByte(s[i:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : i+end]
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func truncateForReason(s string) string {
	const max = 60
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
