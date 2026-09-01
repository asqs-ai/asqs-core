package extendmerge

import (
	"path/filepath"
	"regexp"
	"strings"
)

// JS/TS support for the extend-merge path.
//
// The whole extend pipeline was Java/C#-only: classifyExtendPayload, unwrapCompilationUnit,
// dropDuplicateMembers, mergedPayloadInsideTypeBody and insertInsideClassBody each fell through a
// `default:` arm for every other extension. For a `.ts` artifact that meant a model which ignored
// the members-only contract and returned a COMPLETE file had its whole file appended verbatim to
// the file already on disk — duplicate imports, duplicate `jest.mock`, duplicate `describe`, every
// existing test present twice — and the gate that exists to catch a payload landing outside the
// type body returned true without looking.
//
// The unit of structure here is the top-level `describe(…)` block, which plays the part Java's
// primary type declaration plays: unwrapping a compilation unit means taking that block's body, and
// splicing means inserting before its closing brace.
//
// Everything below bails to "no opinion" the moment it cannot locate structure exactly. The two
// error directions are not symmetric — a false negative leaves today's behaviour, while a false
// positive refuses a write whose artifact has no previous version on disk — which is the same rule
// illegalEscapeReason and SyntacticShellReason follow.

// jsExtendExtensions are the artifact suffixes this file understands. `.tsx` / `.jsx` are absent on
// purpose: JSX puts `<div />` and `</div>` in code position, and the `/` there is neither division
// nor a regex, so the scanner below cannot stay in sync. Those keep the pre-existing behaviour of
// classifyExtendPayload's compilation-unit branch, which refuses the write rather than merging.
var jsExtendExtensions = map[string]bool{
	".ts": true, ".mts": true, ".cts": true,
	".js": true, ".mjs": true, ".cjs": true,
}

// isJSExtendPath reports whether path is a JS/TS artifact this file can reason about.
func isJSExtendPath(path string) bool {
	return jsExtendExtensions[strings.ToLower(filepath.Ext(path))]
}

// jsTopLevelUnitRE matches the constructs that only appear at the top level of a complete module:
// an import, a require assignment, or a jest/vi module mock. Column 0 is what separates "this
// payload IS a file" from "this payload is members of one" — the same test javaTypeDeclAtColumnZero
// applies to a Java type declaration.
var jsTopLevelUnitRE = regexp.MustCompile(`(?m)^(?:import\s|export\s|(?:const|let|var)\s+[\w{}\s,]+=\s*require\s*\(|jest\.mock\s*\(|vi\.mock\s*\()`)

// jsTopLevelDescribeRE matches a suite block opened at column 0, in either dialect: Jest/Vitest/
// Mocha spell it `describe(`, Playwright spells it `test.describe(`. Leaving the second out is what
// made two E2E gaps in run 2026-09-01T08:36Z lose their artifacts — their payload classified as a
// compilation unit (it had imports) and then could not be unwrapped, so the write was refused.
var jsTopLevelDescribeRE = regexp.MustCompile(`(?m)^(?:test\.describe|describe|suite)(?:\.\w+)*\s*\(`)

// jsSuiteKind reports which test dialect a file's top-level suite is written in: "playwright" for
// `test.describe(`, "bdd" for `describe(` / `suite(`, and "" when there is no top-level suite.
//
// The two are not interchangeable. A Playwright spec merged into a Jest suite compiles and then
// fails at run time on a `page` fixture Jest never provides — and that pairing is reachable, because
// an E2E gap whose symbol lives in a source file can resolve its artifact to that file's UNIT test
// path. Refusing the merge is the honest outcome there.
func jsSuiteKind(s string) string {
	loc := jsTopLevelDescribeRE.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	if strings.HasPrefix(s[loc[0]:loc[1]], "test.describe") {
		return "playwright"
	}
	return "bdd"
}

// JSSuiteKindsCompatible reports whether a payload may be merged into the file at hand: both must
// be written in the same test dialect. Unknown on either side is compatible, keeping the historical
// behaviour for shapes this file cannot read.
func JSSuiteKindsCompatible(existing, payload string) bool {
	a, b := jsSuiteKind(existing), jsSuiteKind(payload)
	return a == "" || b == "" || a == b
}

// jsPayloadIsCompilationUnit reports whether a JS/TS extend payload is a complete module rather
// than the members-only body the prompt asked for.
func jsPayloadIsCompilationUnit(s string) bool {
	if jsTopLevelUnitRE.MatchString(s) {
		return true
	}
	// A column-0 describe, but NOT one at offset 0.
	//
	// classifyExtendPayload trims the payload before it gets here, so the first line always starts
	// at column 0 — a members-only payload that happens to begin with a nested `describe('edge
	// cases', …)` would otherwise read as a whole file and be flattened into the target suite. Any
	// LATER line starting at column 0 means the payload has file-level structure, which is what the
	// observed artifact had: eleven lines of imports and jest.mock above its describe.
	for _, loc := range jsTopLevelDescribeRE.FindAllStringIndex(s, -1) {
		if loc[0] == 0 {
			continue
		}
		if _, _, ok := jsPrimaryDescribeBodyRange(s); ok {
			return true
		}
	}
	return false
}

// jsPrimaryDescribeBodyRange returns the byte offsets of the `{` and `}` bounding the body of the
// file's single top-level describe block.
//
// Deliberately single: two top-level describes have no "primary" and splicing into either is a
// guess, so ok is false and the caller falls back to refusing the write.
func jsPrimaryDescribeBodyRange(s string) (open int, closeIdx int, ok bool) {
	if strings.Contains(s, "</") || strings.Contains(s, "/>") {
		return 0, 0, false // JSX: the scanner cannot classify `/` here.
	}
	locs := jsTopLevelDescribeRE.FindAllStringIndex(s, -1)
	if len(locs) != 1 {
		return 0, 0, false
	}
	// The arrow/function body brace is the first `{` at paren depth 1 after the describe's `(`.
	openParen := strings.IndexByte(s[locs[0][0]:], '(')
	if openParen < 0 {
		return 0, 0, false
	}
	openParen += locs[0][0]
	body := jsFirstBraceAfter(s, openParen)
	if body < 0 {
		return 0, 0, false
	}
	end := jsScanToMatchingCloseBrace(s, body)
	if end < 0 {
		return 0, 0, false
	}
	return body, end, true
}

// jsFirstBraceAfter returns the index of the first `{` in code position after i, or -1.
func jsFirstBraceAfter(s string, i int) int {
	prev := byte(0)
	for i < len(s) {
		if next, skipped, ok := jsSkipNonCode(s, i, prev); skipped {
			if !ok {
				return -1
			}
			i = next
			continue
		}
		if s[i] == '{' {
			return i
		}
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			prev = s[i]
		}
		i++
	}
	return -1
}

// jsScanToMatchingCloseBrace is scanToMatchingCloseBrace with JavaScript's lexical rules: template
// literals and regex literals in addition to comments and quoted strings. Returns -1 when the scan
// cannot stay in sync.
func jsScanToMatchingCloseBrace(s string, openBrace int) int {
	if openBrace < 0 || openBrace >= len(s) || s[openBrace] != '{' {
		return -1
	}
	depth := 1
	prev := byte('{')
	for i := openBrace + 1; i < len(s); {
		if next, skipped, ok := jsSkipNonCode(s, i, prev); skipped {
			if !ok {
				return -1
			}
			prev = s[next-1]
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
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			prev = s[i]
		}
		i++
	}
	return -1
}

// jsRegexOperandLeads are the bytes after which a `/` opens a regex literal rather than dividing.
// This is the standard lexer heuristic: a regex may only appear where an operand is expected.
var jsRegexOperandLeads = map[byte]bool{
	0: true, '(': true, ',': true, '=': true, ':': true, '[': true, '!': true, '&': true,
	'|': true, '?': true, '{': true, '}': true, ';': true, '+': true, '-': true, '*': true,
	'%': true, '~': true, '^': true, '<': true, '>': true,
}

// jsSkipNonCode advances past one comment, string, template literal or regex literal beginning at
// i. skipped is false when i is ordinary code; ok is false when the construct never terminates,
// which means the scan has lost sync and every caller must give up.
//
// prev is the last significant byte before i, and is used only to decide whether `/` opens a regex
// or divides — the one genuinely ambiguous token in the language.
func jsSkipNonCode(s string, i int, prev byte) (next int, skipped bool, ok bool) {
	if i >= len(s) {
		return i, false, true
	}
	switch {
	case i+1 < len(s) && s[i] == '/' && s[i+1] == '/':
		for i < len(s) && s[i] != '\n' {
			i++
		}
		return i, true, true

	case i+1 < len(s) && s[i] == '/' && s[i+1] == '*':
		i += 2
		for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
			i++
		}
		if i+1 >= len(s) {
			return i, true, false
		}
		return i + 2, true, true

	case s[i] == '/' && jsRegexOperandLeads[prev]:
		// Regex literal. A class `[...]` may contain an unescaped `/`, so track it.
		i++
		inClass := false
		for i < len(s) {
			switch s[i] {
			case '\\':
				i++
			case '[':
				inClass = true
			case ']':
				inClass = false
			case '\n':
				return i, true, false // regex literals cannot span lines
			case '/':
				if !inClass {
					return i + 1, true, true
				}
			}
			i++
		}
		return i, true, false

	case s[i] == '"' || s[i] == '\'':
		quote := s[i]
		i++
		for i < len(s) {
			if s[i] == '\\' {
				i += 2
				continue
			}
			if s[i] == '\n' {
				return i, true, false
			}
			if s[i] == quote {
				return i + 1, true, true
			}
			i++
		}
		return i, true, false

	case s[i] == '`':
		i++
		for i < len(s) {
			if s[i] == '\\' {
				i += 2
				continue
			}
			if s[i] == '`' {
				return i + 1, true, true
			}
			// `${ … }` holds ordinary code, including braces and nested templates. Hand it back to
			// the caller's depth counter by skipping to the matching `}` with this same scanner.
			if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
				end := jsScanToMatchingCloseBrace(s, i+1)
				if end < 0 {
					return i, true, false
				}
				i = end + 1
				continue
			}
			i++
		}
		return i, true, false
	}
	return i, false, true
}

// jsMember is one direct child of a describe body: a declaration, a lifecycle hook, a test, or a
// nested describe. Key is the identity two members are compared on; it is empty for anything this
// file does not recognise, and an unrecognised member is never dropped.
type jsMember struct {
	Start, End int
	Key        string
}

var (
	jsTestCallRE  = regexp.MustCompile(`^(it|test|xit|fit|xtest|specify)(?:\.\w+)*\s*\(\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
	jsSuiteCallRE = regexp.MustCompile(`^(describe|suite|xdescribe|fdescribe)(?:\.\w+)*\s*\(\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
	jsHookCallRE  = regexp.MustCompile(`^(beforeEach|afterEach|beforeAll|afterAll|before|after|setup|teardown)\s*\(`)
	jsDeclRE      = regexp.MustCompile(`^(?:const|let|var)\s+([A-Za-z_$][\w$]*)`)
)

// jsMemberKey identifies a describe-body member. Tests and nested suites are keyed on their TITLE:
// JavaScript has no method name to compare, and the title is what a reader and a test report both
// treat as the test's identity.
func jsMemberKey(stmt string) string {
	s := strings.TrimSpace(stmt)
	pick := func(m []string) string {
		for _, g := range m[2:] {
			if g != "" {
				return g
			}
		}
		return ""
	}
	if m := jsTestCallRE.FindStringSubmatch(s); m != nil {
		if title := pick(m); title != "" {
			return "test:" + title
		}
		return ""
	}
	if m := jsSuiteCallRE.FindStringSubmatch(s); m != nil {
		if title := pick(m); title != "" {
			return "suite:" + title
		}
		return ""
	}
	if m := jsHookCallRE.FindStringSubmatch(s); m != nil {
		return "hook:" + m[1]
	}
	if m := jsDeclRE.FindStringSubmatch(s); m != nil {
		return "decl:" + m[1]
	}
	return ""
}

// jsSplitMembers splits a describe body into its direct children.
//
// A member ends at the `;` that closes it at nesting depth 0. Generated test files are formatter
// output and always carry that terminator; a body this cannot split completely returns ok=false so
// the caller refuses the merge instead of splicing a half-parsed payload.
func jsSplitMembers(body string) ([]jsMember, bool) {
	var out []jsMember
	i, n := 0, len(body)
	prev := byte(0)
	for i < n {
		for i < n && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
			i++
		}
		if i >= n {
			break
		}
		start := i
		depth := 0
		end := -1
		for i < n {
			if next, skipped, ok := jsSkipNonCode(body, i, prev); skipped {
				if !ok {
					return nil, false
				}
				prev = body[next-1]
				i = next
				continue
			}
			c := body[i]
			switch c {
			case '{', '(', '[':
				depth++
			case '}', ')', ']':
				depth--
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				prev = c
			}
			i++
			if c == ';' && depth == 0 {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, false
		}
		out = append(out, jsMember{Start: start, End: end, Key: jsMemberKey(body[start:end])})
	}
	return out, true
}

// jsDropDuplicateMembers removes payload members the target's describe body already declares.
//
// Without it the unwrapped payload re-declares the suite's `let` bindings and its beforeEach, which
// TypeScript rejects as a redeclaration — so this is part of the merge being correct, not a
// tidiness pass. On the artifact that prompted this code it reduced an eight-test payload to the
// three tests the second gap actually added.
func jsDropDuplicateMembers(existingFile, payload string) (string, []string, bool) {
	open, closeIdx, ok := jsPrimaryDescribeBodyRange(existingFile)
	if !ok {
		return payload, nil, false
	}
	existingMembers, ok := jsSplitMembers(existingFile[open+1 : closeIdx])
	if !ok {
		return payload, nil, false
	}
	taken := make(map[string]bool, len(existingMembers))
	for _, m := range existingMembers {
		if m.Key != "" {
			taken[m.Key] = true
		}
	}
	payloadMembers, ok := jsSplitMembers(payload)
	if !ok {
		return payload, nil, false
	}
	var kept []string
	var dropped []string
	for _, m := range payloadMembers {
		if m.Key != "" && taken[m.Key] {
			dropped = append(dropped, m.Key)
			continue
		}
		kept = append(kept, strings.TrimRight(payload[m.Start:m.End], " \t\n\r"))
	}
	return strings.Join(kept, "\n\n"), dropped, true
}

// jsInsertBeforeDescribeClose splices payload in just before the primary describe's closing brace,
// indented to match the body it joins. ok is false when the block cannot be located, which leaves
// the caller on its append-at-EOF fallback.
func jsInsertBeforeDescribeClose(existing, payload string) (string, bool) {
	s := strings.ReplaceAll(existing, "\r\n", "\n")
	_, closeIdx, ok := jsPrimaryDescribeBodyRange(s)
	if !ok {
		return "", false
	}
	lineStart := strings.LastIndex(s[:closeIdx], "\n") + 1
	indent := s[lineStart:closeIdx]
	if strings.TrimSpace(indent) != "" {
		indent = ""
	}
	return s[:closeIdx] + indentLines(strings.TrimSpace(payload), indent+"  ") + "\n" + s[closeIdx:], true
}
