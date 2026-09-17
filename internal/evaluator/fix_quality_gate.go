package evaluator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	reFixLowValueTypeOfNotNull = regexp.MustCompile(`(?is)typeof\s*\(.*?\).*?Assert\.NotNull\s*\(`)
	reFixLowValueSelfSmoke     = regexp.MustCompile(`(?is)Assert\.NotNull\s*\(\s*new\s+[A-Za-z_][A-Za-z0-9_]*Tests?\s*\(`)
	// A skipped test with an empty body, in all three runners' spellings. xUnit says
	// [Fact(Skip="…")]; NUnit and MSTest both use [Ignore], with or without a reason, and NUnit
	// adds [Explicit] for a test that only runs when named. All of them produce a shell that
	// reports as a pass and asserts nothing.
	reFixLowValueCSharpSkip = regexp.MustCompile(
		`(?is)\[(?:(?:Fact|Theory)\s*\(\s*Skip\s*=[^)]*\)|Ignore(?:\s*\([^)]*\))?|Explicit(?:\s*\([^)]*\))?)\][\s\S]{0,160}?\{\s*(?://[^\n]*\n|\s)*\}`)
	reFixLowValueJavaSkip      = regexp.MustCompile(`(?is)@(Disabled|Ignore|Ignored)\b[\s\S]{0,160}?\{\s*(?://[^\n]*\n|\s)*\}`)
	reFixLowValueJSSkip        = regexp.MustCompile(`(?is)\b(?:it|test|describe)\.skip\s*\([^,]+,\s*(?:async\s*)?(?:\(\s*\)\s*=>|function\s*\(\s*\))\s*\{\s*(?://[^\n]*\n|\s)*\}\s*\)`)
	reFixLowValueAssertTrue    = regexp.MustCompile(`(?i)\bAssert\.True\s*\(\s*true\s*\)`)
	reFixLowValueAssertFalse   = regexp.MustCompile(`(?i)\bAssert\.False\s*\(\s*false\s*\)`)
	reFixLowValueExpectTrue    = regexp.MustCompile(`(?i)\bexpect\s*\(\s*true\s*\)\s*\.\s*toBe\s*\(\s*true\s*\)`)
	reFixLowValueExpectFalse   = regexp.MustCompile(`(?i)\bexpect\s*\(\s*false\s*\)\s*\.\s*toBe\s*\(\s*false\s*\)`)
	reFixLowValuePlaceholder   = regexp.MustCompile(`(?is)^\s*(?:(?://[^\n]*|/\*[\s\S]*?\*/)\s*)+$`)
	reFixNoOpMarker            = regexp.MustCompile(`(?i)\bno[- ]?op\b|\bplaceholder\b|\btodo\b|\bfix later\b`)
	reFixLowValueCSharpShell   = regexp.MustCompile(`(?is)^\s*(?:using\s+[A-Za-z0-9_.]+\s*;\s*)*(?:namespace\s+[A-Za-z0-9_.]+\s*\{\s*)*(?:public|internal|private|protected|sealed|abstract|static|\s)*class\s+[A-Za-z0-9_]+\s*(?::[^{]+)?\{\s*\}\s*(?:\}\s*)*$`)
	reFixLowValueTypeMetadata  = regexp.MustCompile(`(?is)typeof\s*\([^)]+\)\s*\.\s*(Namespace|Name)\b`)
	reFixLowValueCtorNullGuard = regexp.MustCompile(`(?is)Assert\.(?:ThrowsException<\s*ArgumentNullException\s*>|Throws<\s*ArgumentNullException\s*>)\s*\(\s*\(\)\s*=>\s*new\s+[A-Za-z_][A-Za-z0-9_]*\s*\([^)]*\bnull\b[^)]*\)\s*\)`)

	// Language-specific test-method / test-function markers used by EmptyTestFileReason.
	// Match anywhere in the file — comments/strings containing the literal marker are a vanishingly
	// rare false negative and much cheaper than a full lexer.
	reEmptyTestMarkerJava   = regexp.MustCompile(`(?m)@(?:Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate)\b`)
	reEmptyTestMarkerCSharp = regexp.MustCompile(`\[(?:Fact|Theory|Test|TestCase|TestMethod|DataTestMethod)\b`)
	reEmptyTestMarkerJS     = regexp.MustCompile(`\b(?:it|test|fit|xit)\s*[.(]`)
	reEmptyTestMarkerGo     = regexp.MustCompile(`(?m)^func\s+(?:Test|Benchmark|Example|Fuzz)[A-Z_]\w*\s*\(`)

	// Top-level type-declaration regexes for SyntacticShellReason. They match a modifier-and-
	// annotation prefix followed by one of the type keywords — multiline so we only accept the
	// keyword appearing at the start of a logical line (anchors within a package/namespace body
	// are fine because interior indentation still matches ^\s*). Strings and comments are
	// stripped before the regex runs so a literal "class" inside a String won't false-positive.
	reJavaTypeDecl   = regexp.MustCompile(`(?m)^\s*(?:@\w[\w.]*(?:\s*\([^)]*\))?\s+)*(?:(?:public|private|protected|static|final|abstract|sealed|non-sealed|strictfp|default)\s+)*(?:class|interface|enum|record|@interface)\s+\w`)
	reCSharpTypeDecl = regexp.MustCompile(`(?m)^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|internal|private|protected|sealed|static|abstract|partial|readonly|ref|unsafe)\s+)*(?:class|interface|struct|enum|record|delegate)\s+\w`)
	reGoPackageDecl  = regexp.MustCompile(`(?m)^package\s+\w+`)
)

func lowValueTestContentReason(path, content string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}
	if reFixLowValueTypeOfNotNull.MatchString(s) {
		return "reflection existence assertion (typeof + Assert.NotNull)"
	}
	if reFixLowValueSelfSmoke.MatchString(s) {
		return "self-smoke test (asserting test class instance)"
	}
	if reFixLowValueCSharpSkip.MatchString(s) {
		return "empty skipped xUnit test shell"
	}
	if reFixLowValueJavaSkip.MatchString(s) {
		return "empty disabled Java test shell"
	}
	if reFixLowValueJSSkip.MatchString(s) {
		return "empty skipped JS/TS test shell"
	}
	if reFixLowValueAssertTrue.MatchString(s) || reFixLowValueAssertFalse.MatchString(s) ||
		reFixLowValueExpectTrue.MatchString(s) || reFixLowValueExpectFalse.MatchString(s) {
		return "tautological always-pass assertion"
	}
	if reFixLowValuePlaceholder.MatchString(s) || reFixNoOpMarker.MatchString(s) && !strings.Contains(strings.ToLower(s), "assert") {
		return "placeholder/no-op content without behavioral test logic"
	}
	if reFixLowValueTypeMetadata.MatchString(s) &&
		(strings.Contains(strings.ToLower(s), "assert.equal") ||
			strings.Contains(strings.ToLower(s), "assert.areequal") ||
			strings.Contains(strings.ToLower(s), "assert.isnotnull") ||
			strings.Contains(strings.ToLower(s), "assert.notnull")) {
		return "type-metadata assertion only (typeof(...).Namespace/Name)"
	}
	if reFixLowValueCtorNullGuard.MatchString(s) &&
		!strings.Contains(strings.ToLower(s), "verify(") &&
		!strings.Contains(strings.ToLower(s), "mock.") {
		return "constructor null-guard smoke test without behavioral assertions"
	}
	if strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".cs") &&
		reFixLowValueCSharpShell.MatchString(s) &&
		!strings.Contains(s, "[Fact") &&
		!strings.Contains(s, "[Theory") &&
		!strings.Contains(s, "[TestMethod") &&
		!strings.Contains(s, "Assert.") {
		return "empty C# namespace/class scaffold without test methods"
	}
	// Extra guard: path names that scream self-smoke while only asserting NotNull/defined.
	base := strings.ToLower(filepath.Base(path))
	if (strings.Contains(base, "smoke") || strings.Contains(base, "reference")) &&
		(strings.Contains(strings.ToLower(s), "assert.notnull") || strings.Contains(strings.ToLower(s), ".tobedefined(")) {
		return "smoke/reference-only non-behavioral test"
	}
	return ""
}

// EmptyTestFileReason returns a non-empty reason when `content` for the given test-file `path` has
// no actual test methods / test calls and must therefore not be written or accepted. This is an
// **absolute** gate (unlike introducedLowValueFixReason which only rejects quality regressions):
// a file like
//
//	package org.springframework.samples.petclinic.owner;
//
//	class OwnerControllerE2EIT {
//	}
//
// has a Java package + class shell but no @Test method, so running it adds zero coverage while still
// consuming a test artifact slot. Detection is purely marker-based per language extension; unknown
// extensions return "" (no opinion).
//
// Markers:
//   - .java                                     → @Test / @ParameterizedTest / @RepeatedTest / @TestFactory / @TestTemplate
//   - .cs                                       → [Fact / [Theory / [Test] / [TestCase / [TestMethod / [DataTestMethod
//   - .ts / .tsx / .js / .jsx / .mjs / .cjs     → it(…) / test(…) / it.each / test.only / fit / xit
//   - *_test.go                                 → func TestXxx / BenchmarkXxx / ExampleXxx / FuzzXxx
//
// Returns "" for whitespace-only content so the caller can use a distinct audit event for that case.
func EmptyTestFileReason(path, content string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".java"):
		if !reEmptyTestMarkerJava.MatchString(s) {
			return "empty Java test file (no @Test/@ParameterizedTest/@RepeatedTest/@TestFactory method)"
		}
	case strings.HasSuffix(base, ".cs"):
		if !reEmptyTestMarkerCSharp.MatchString(s) {
			return "empty C# test file (no [Fact]/[Theory]/[Test]/[TestMethod] attributed test)"
		}
	case strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx") ||
		strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".jsx") ||
		strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs"):
		if !reEmptyTestMarkerJS.MatchString(s) {
			return "empty JS/TS test file (no it()/test()/fit()/xit() call)"
		}
	case strings.HasSuffix(base, "_test.go"):
		if !reEmptyTestMarkerGo.MatchString(s) {
			return "empty Go test file (no Test/Benchmark/Example/Fuzz function)"
		}
	}
	return ""
}

// introducedLowValueFixReason returns non-empty only when fixer output degrades quality
// (new file content matches low-value patterns while previous content did not).
func introducedLowValueFixReason(path, before, after string) string {
	afterReason := lowValueTestContentReason(path, after)
	if afterReason == "" {
		return ""
	}
	if lowValueTestContentReason(path, before) != "" {
		return ""
	}
	return afterReason
}

// SyntacticShellReason returns a non-empty reason when `content` is obviously syntactically
// malformed for its file extension, BEFORE the actual compiler sees it. This is a cheap
// pre-flight check that catches the two most common "LLM returned garbage" failure modes
// observed in production fix loops:
//
//  1. Markdown code fences (```java / ``` / ``` at BOF/EOF) leak into the generated source so
//     javac / roslyn barf with "class, interface, enum, or record expected" at line 2 col 1.
//  2. Source truncation / mis-nesting leaves brace counts unbalanced — the compiler will report
//     the mismatch eventually but we burn a whole fix-loop iteration and a full LLM call first.
//
// Detection is conservative by design (false negatives are preferred over false positives —
// the compiler is still the authoritative check): unknown extensions return "", and the Java/C#
// top-level-declaration check only fires when the file contains ZERO type declarations (a real
// file with extra top-level garbage BEFORE the class is caught by the brace/fence checks but not
// by the declaration regex). Returns "" for whitespace-only content so the caller can emit
// evaluator.fix_skip_empty instead.
//
// TypeScript / JavaScript get the fence check plus a stray-backslash scan, and NOT brace counting:
// template-literal interpolation (`${…}`) makes counting braces unsafe, which is why this
// extension was excluded outright until a run proved the exclusion too broad. See
// jsSyntacticShellReason.
//
// Supported languages: Java (.java), C# (.cs), Go (.go), TypeScript / JavaScript
// (.ts, .tsx, .mts, .cts, .js, .jsx, .mjs, .cjs).
func SyntacticShellReason(path, content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".java"):
		return javaSyntacticShellReason(content)
	case strings.HasSuffix(base, ".cs"):
		return csharpSyntacticShellReason(content)
	case strings.HasSuffix(base, ".go"):
		return goSyntacticShellReason(content)
	case hasJSExtension(base):
		return jsSyntacticShellReason(content)
	}
	return ""
}

// jsExtensions are the suffixes jsSyntacticShellReason understands. .tsx / .jsx are included: the
// scan bails on JSX before it can misread it (see jsSyntacticShellReason), so the fence check still
// covers them and nothing else fires.
var jsExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

func hasJSExtension(base string) bool {
	for _, ext := range jsExtensions {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

func javaSyntacticShellReason(s string) string {
	if strings.Contains(s, "```") {
		return "contains markdown code fence (```), LLM emitted fenced output instead of raw Java source"
	}
	stripped := stripStringsAndComments(s, ".java")
	if op, cl := strings.Count(stripped, "{"), strings.Count(stripped, "}"); op != cl {
		return fmt.Sprintf("unbalanced braces ({=%d, }=%d), Java source is truncated or mis-nested", op, cl)
	}
	// Parens, counted after the braces because a truncated file is short both and "truncated" is
	// the more useful of the two diagnoses.
	//
	// Braces alone cannot see a dropped `)`: `if (result.contains("x") {` opens and closes its
	// block correctly and is short exactly one paren. asqs-go run
	// api-c7682cc0710205f1bed996c3095eaf8f wrote LegacyOrderFormatterTest.java with
	// `[37,45] ')' expected` and `[52,47] ')' expected`, the fixer rewrote the file four times
	// without touching either site, the loop stopped and the run shipped nothing. Nothing between
	// the model and javac was looking.
	//
	// Safe because the count is taken from `stripped`: parens inside string literals, text blocks,
	// char literals and comments are already gone, and every construct that legitimately nests
	// them — calls, lambdas, casts, annotations, control-flow heads — balances by definition.
	if op, cl := strings.Count(stripped, "("), strings.Count(stripped, ")"); op != cl {
		return fmt.Sprintf("unbalanced parentheses ((=%d, )=%d), Java source will not parse", op, cl)
	}
	// Lexical, so it belongs beside the bracket counts and ahead of the structural checks below:
	// a character javac will not accept is not a file with a missing declaration, it is a file the
	// lexer never gets through.
	if reason := javaStrayCharacterReason(stripped); reason != "" {
		return reason
	}
	if !reJavaTypeDecl.MatchString(stripped) {
		return "no class/interface/enum/record declaration, file will not parse as Java"
	}
	return ""
}

// javaCodePunct is every ASCII punctuation and operator character Java accepts in code position.
// `->`, `::`, `...`, generics and annotations are all built from members of this set.
const javaCodePunct = "(){}[];,.=<>!~?:+-*/&|^%@"

// csharpCodePunct is the C# set: Java's plus `#` for preprocessor directives and `$` for the
// interpolation marker, which survives stripStringsAndComments in code position. Ranges (`..`),
// index-from-end (`^`), null-coalescing (`??=`), lambdas (`=>`) and attributes (`[Fact]`) are all
// built from members already present.
const csharpCodePunct = javaCodePunct + "#$"

// javaStrayCharacterReason reports the first character javac would reject outright as an
// `illegal character`, or "" when the file is clean or the scan declines.
//
// asqs-go run api-c7682cc0710205f1bed996c3095eaf8f's own audit carries a U+2026 — its refused fix anchor
// read "…returnsFormatted…" — and asqs-go run api-f1d4227cb6db875a2e51c3100b3e1be8 shipped a `\ ` where
// `\n` belonged, straight out of the structured-JSON envelope. Both reached disk.
//
// Whitespace is the JLS set and deliberately NOT unicode.IsSpace, which would admit the
// non-breaking space javac refuses. This is where the C# arm diverges — see
// csharpStrayCharacterReason.
//
// Text blocks need no special handling here: stripStringsAndComments consumes them, so their
// contents never reach this scan.
func javaStrayCharacterReason(stripped string) string {
	return strayCharacterReason(stripped, javaCodePunct, isJLSWhitespace, "javac")
}

// csharpStrayCharacterReason is the C# counterpart. It is NOT the Java check with a different punct
// set: Roslyn accepts the non-breaking space javac rejects (Unicode Zs is whitespace in C#), so it
// uses unicode.IsSpace and a U+00A0 that would be refused on the Java side passes here. Verified
// against dotnet rather than assumed.
func csharpStrayCharacterReason(stripped string) string {
	return strayCharacterReason(stripped, csharpCodePunct, unicode.IsSpace, "the C# compiler")
}

// isJLSWhitespace is the whitespace set JLS 3.6 defines: space, tab, form feed, and the line
// terminators. Narrower than unicode.IsSpace on purpose.
func isJLSWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\f' || r == '\n'
}

// strayCharacterReason scans code-position text for a character the language's lexer rejects.
//
// The test is "not legal in code position", NOT "not ASCII": both languages allow Unicode letters
// in identifiers, so `String café = …` is valid and rejecting it would destroy a correct artifact.
// `\uXXXX` is legal in code position in both and is skipped rather than reported.
func strayCharacterReason(stripped, punct string, space func(rune) bool, compiler string) string {
	line, col := 1, 0
	for i := 0; i < len(stripped); {
		r, size := utf8.DecodeRuneInString(stripped[i:])
		if r == '\n' {
			line++
			col = 0
			i += size
			continue
		}
		col++
		switch {
		case space(r):
		case r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r):
		case strings.ContainsRune(punct, r):
		case r == '\\':
			if n := unicodeEscapeLen(stripped[i:]); n > 0 {
				col += n - 1
				i += n
				continue
			}
			return fmt.Sprintf("stray backslash at line %d column %d, outside any string, char literal or comment; %s rejects it as an illegal character", line, col, compiler)
		default:
			return fmt.Sprintf("illegal character %q at line %d column %d, outside any string, char literal or comment; %s rejects it", r, line, col, compiler)
		}
		i += size
	}
	return ""
}

// unicodeEscapeLen returns the byte length of a `\uXXXX` escape at the start of s, or 0.
// JLS 3.3 allows any number of `u`s after the backslash; C# accepts the single-`u` form.
func unicodeEscapeLen(s string) int {
	if len(s) < 2 || s[0] != '\\' || s[1] != 'u' {
		return 0
	}
	i := 1
	for i < len(s) && s[i] == 'u' {
		i++
	}
	if i+4 > len(s) {
		return 0
	}
	for j := i; j < i+4; j++ {
		c := s[j]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return 0
		}
	}
	return i + 4
}

// utf8BOM is the byte-order mark, which C# permits at the start of a compilation unit and nowhere
// else.
const utf8BOM = "\ufeff"

func csharpSyntacticShellReason(s string) string {
	if strings.Contains(s, "```") {
		return "contains markdown code fence (```), LLM emitted fenced output instead of raw C# source"
	}
	// Dropped once, here, so every C# check below reads the same text. A UTF-8 BOM opens a large
	// share of the .cs files in the world — Visual Studio writes one by default — so an extend-mode
	// payload merged into an existing repository file inherits that file's own mark. A validation
	// run lost FIVE of its fifteen gaps to it: every E2E payload was such a merge, and each was
	// refused as an illegal character with the claim that the compiler rejects it. It does not.
	// Measured both ways: `dotnet build` on a .cs whose first three bytes are the BOM reports 0
	// errors, while javac on the same shape reports `error: illegal character: '\ufeff'` — which is
	// why this is the C# arm's business alone and javaSyntacticShellReason is untouched.
	//
	// Only the leading one. A BOM anywhere else is a stray character in code position and Roslyn
	// does reject it, so the scan below must still see those.
	s = strings.TrimPrefix(s, utf8BOM)
	stripped := stripStringsAndComments(s, ".cs")
	if op, cl := strings.Count(stripped, "{"), strings.Count(stripped, "}"); op != cl {
		return fmt.Sprintf("unbalanced braces ({=%d, }=%d), C# source is truncated or mis-nested", op, cl)
	}
	if op, cl := strings.Count(stripped, "("), strings.Count(stripped, ")"); op != cl {
		return fmt.Sprintf("unbalanced parentheses ((=%d, )=%d), C# source will not parse", op, cl)
	}
	if reason := csharpStrayCharacterReason(stripped); reason != "" {
		return reason
	}
	if !reCSharpTypeDecl.MatchString(stripped) {
		return "no class/interface/struct/enum/record/delegate declaration, file will not parse as C#"
	}
	if reason := csharpStrayTokenBeforeTypeReason(stripped); reason != "" {
		return reason
	}
	if reason := CSharpStatementStructureReason(s); reason != "" {
		return reason
	}
	return ""
}

// jsSyntacticShellReason is the TypeScript / JavaScript branch of the pre-write gate.
//
// It is NOT the Java/C# check with a different extension list. Brace counting is deliberately
// absent: `${…}` interpolation inside a template literal makes it unsafe, and that is why this
// language was excluded from the gate entirely until run api-f1d4227cb6db875a2e51c3100b3e1be8
// showed the exclusion was too broad. That run generated
// src/app/features/checkout/checkout.component.test.ts containing `const quantity = 5;\      const`
// — the model's structured JSON output carried an illegal `\ ` escape where `\n` belonged — and
// nothing looked at the file: the eval's `npm test` ran the repository's own echo script, `ng build`
// compiles only the graph reachable from main.ts, and prettier was not installed. The broken file
// went into the PR.
//
// So the two checks here are the ones that cannot be wrong about JavaScript:
//
//   - a markdown fence, which is never valid source in any language;
//   - a backslash sitting in code position, which no JS/TS grammar accepts. Every legal backslash
//     lives inside a string, a template literal, a regex, or a comment.
//
// It runs on BOTH the generate and fix paths, for the same reason the fence check does: it detects
// content that is UNUSABLE rather than broken. A backslash in code position cannot be repaired by a
// later fix round, because the file it produces never reaches a compiler that would report it.
func jsSyntacticShellReason(s string) string {
	if strings.Contains(s, "```") {
		return "contains markdown code fence (```), LLM emitted fenced output instead of raw TypeScript/JavaScript source"
	}
	if line, col, found := jsStrayBackslash(s); found {
		return fmt.Sprintf("stray backslash at line %d column %d, outside any string, template literal, "+
			"regex or comment; no JS/TS grammar accepts it and the file will not parse", line, col)
	}
	return ""
}

// jsStrayBackslash reports the position of the first backslash in CODE position, or found=false
// when the file is clean OR when the scanner cannot stay in sync.
//
// The two error directions cost very different amounts — a false negative costs one compile cycle,
// a false positive silently discards a correct artifact — so this bails out (found=false) on every
// construct it cannot tokenize exactly, exactly as illegalEscapeReason does:
//
//   - a `/` in code position that begins neither `//` nor `/*`. It could open a regex literal, be a
//     division, or close a JSX tag, and guessing wrong desynchronises everything after it. This is
//     what makes .tsx effectively fence-only, which is the intended trade.
//   - a string literal left unterminated at a newline or at EOF, and an unterminated block comment
//     or template — all of which mean sync is already lost.
func jsStrayBackslash(s string) (line, col int, found bool) {
	line, col = 1, 1
	advance := func(c byte) {
		if c == '\n' {
			line++
			col = 1
			return
		}
		col++
	}
	// templateBrace holds one entry per `${` interpolation currently open; the value counts the
	// unclosed `{` inside it, so the matching `}` returns the scanner to template text.
	var templateBrace []int
	inTemplate := false

	for i, n := 0, len(s); i < n; {
		c := s[i]
		if inTemplate {
			switch {
			case c == '\\':
				if i+1 >= n {
					return 0, 0, false
				}
				advance(c)
				advance(s[i+1])
				i += 2
				continue
			case c == '`':
				inTemplate = false
			case c == '$' && i+1 < n && s[i+1] == '{':
				templateBrace = append(templateBrace, 0)
				inTemplate = false
				advance(c)
				advance(s[i+1])
				i += 2
				continue
			}
			advance(c)
			i++
			continue
		}

		switch c {
		case '/':
			if i+1 >= n {
				return 0, 0, false
			}
			switch s[i+1] {
			case '/':
				for i < n && s[i] != '\n' {
					advance(s[i])
					i++
				}
			case '*':
				advance(c)
				advance(s[i+1])
				i += 2
				for {
					if i+1 >= n {
						return 0, 0, false
					}
					if s[i] == '*' && s[i+1] == '/' {
						advance(s[i])
						advance(s[i+1])
						i += 2
						break
					}
					advance(s[i])
					i++
				}
			default:
				return 0, 0, false
			}
			continue
		case '\'', '"':
			quote := c
			advance(c)
			i++
			for {
				if i >= n || s[i] == '\n' {
					return 0, 0, false
				}
				if s[i] == '\\' {
					if i+1 >= n {
						return 0, 0, false
					}
					advance(s[i])
					advance(s[i+1])
					i += 2
					continue
				}
				closing := s[i] == quote
				advance(s[i])
				i++
				if closing {
					break
				}
			}
			continue
		case '`':
			inTemplate = true
		case '{':
			if len(templateBrace) > 0 {
				templateBrace[len(templateBrace)-1]++
			}
		case '}':
			if top := len(templateBrace) - 1; top >= 0 {
				if templateBrace[top] == 0 {
					templateBrace = templateBrace[:top]
					inTemplate = true
				} else {
					templateBrace[top]--
				}
			}
		case '\\':
			return line, col, true
		}
		advance(c)
		i++
	}
	return 0, 0, false
}

func goSyntacticShellReason(s string) string {
	if !reGoPackageDecl.MatchString(s) {
		return "missing package declaration, file will not parse as Go"
	}
	stripped := stripStringsAndComments(s, ".go")
	if op, cl := strings.Count(stripped, "{"), strings.Count(stripped, "}"); op != cl {
		return fmt.Sprintf("unbalanced braces ({=%d, }=%d), Go source is truncated or mis-nested", op, cl)
	}
	return ""
}

// csharpUnmodelledLiteralEnd returns the index just past a C# string literal whose escape rules the
// escape scanners do not model, and whether the position starts one.
//
// Three forms qualify, and all three share one property: a backslash inside them is a literal
// backslash, so `C:\dir` is correct code rather than an illegal escape.
//
//   - verbatim, in either modifier order: @"…", $@"…", @$"…"
//   - raw string literals: """…"""
//
// `$"…"` is deliberately NOT one of them: an interpolated non-verbatim string processes escapes
// under the ordinary rules, so the ordinary scan is correct for it.
//
// This replaces a whole-file bail. Seeing any of these anywhere switched the escape gate off for
// the entire file, and a C# test of a repository layer contains a verbatim SQL string or Windows
// path as a matter of course — so the check that exists to catch an unrepaired `\d` was disabled
// in exactly the files most likely to have one.
func csharpUnmodelledLiteralEnd(s string, i int) (int, bool) {
	switch {
	case s[i] == '@':
	case s[i] == '$' && i+1 < len(s) && s[i+1] == '@':
	case strings.HasPrefix(s[i:], `"""`):
	default:
		return 0, false
	}
	return stringLiteralEnd(s, i, true)
}

// stringLiteralEnd returns the index just past the string literal starting at i, or ok=false when
// i does not start one. Handles the C#/Java forms the checks actually meet:
//
//   - a prefix of at most one `@` and one `$` in either order, C# only;
//   - a raw string / Java text block, opened by three or more quotes and closed by a run of the
//     same length — never with an `@` prefix, where `""` is an escaped quote instead;
//   - a verbatim string (`@`-prefixed), in which `\` is an ordinary character and `""` is an
//     escaped quote;
//   - an ordinary string, in which `\` escapes the next character.
//
// An unterminated literal consumes to end of input, which is what the old scanner did and is the
// safe direction: the tail is treated as string rather than as code.
func stringLiteralEnd(s string, i int, javaOrCS bool) (int, bool) {
	n := len(s)
	j := i
	verbatim := false
	if javaOrCS {
		seenAt, seenDollar := false, false
		for j < n && (s[j] == '@' || s[j] == '$') {
			if s[j] == '@' {
				if seenAt {
					break
				}
				seenAt, verbatim = true, true
			} else {
				if seenDollar {
					break
				}
				seenDollar = true
			}
			j++
		}
	}
	if j >= n || s[j] != '"' {
		return 0, false
	}
	quotes := 0
	for j+quotes < n && s[j+quotes] == '"' {
		quotes++
	}
	if quotes >= 3 && !verbatim && javaOrCS {
		// Raw string / text block. Java and C# only: Go has no such form, where `""` is an empty
		// string and the third quote opens a new one, so claiming the run would swallow the rest of
		// the file.
		//
		// Scan for a closing run of at least `quotes` quotes.
		k := j + quotes
		for k < n {
			if s[k] != '"' {
				k++
				continue
			}
			run := 0
			for k+run < n && s[k+run] == '"' {
				run++
			}
			if run >= quotes {
				return k + run, true
			}
			k += run
		}
		return n, true
	}
	k := j + 1
	for k < n {
		switch {
		case !verbatim && s[k] == '\\' && k+1 < n:
			k += 2
		case s[k] == '"':
			if verbatim && k+1 < n && s[k+1] == '"' {
				k += 2
				continue
			}
			return k + 1, true
		default:
			k++
		}
	}
	return n, true
}

// stripStringsAndComments is a cheap approximate tokenizer that replaces the contents of string
// literals, char literals, and // / /* */ comments with a placeholder byte so the checks built on
// it — brace and paren balance, stray characters, the type-declaration regexes — reason about code
// and not about text that merely looks like code.
//
// It handles Java text blocks and C# verbatim, raw and interpolated strings, in any legal prefix
// order (`@"`, `$"`, `@$"`, `$@"`, `"""`, `$"""`). It did not, and the gap was not cosmetic: the
// old scanner read `\"` as an escape in every string form, so `@"C:\"` — legal C# whose content
// is one backslash — ran past its real terminator and closed on the NEXT quote. Everything between
// became code, which is how SyntacticShellReason came to refuse Roslyn-valid source with
// "unbalanced braces ({=2, }=0)" and would have refused it again for a paren or a stray character
// drawn from string contents.
//
// Supported extensions: `.java`, `.cs` (// and /* */ comments, the string forms above, and
// single-quoted char literals), `.go` (adds backtick raw strings).
//
// Still approximate in one safe direction: the expression inside a `{…}` interpolation hole is
// swallowed with the rest of the literal rather than kept as code, so a defect there is missed
// rather than invented.
func stripStringsAndComments(s, ext string) string {
	javaOrCS := ext == ".java" || ext == ".cs"
	goExt := ext == ".go"
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		if c == '/' && i+1 < n {
			if s[i+1] == '/' {
				if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
					b.WriteByte('\n')
					i += j + 1
					continue
				}
				return b.String()
			}
			if s[i+1] == '*' {
				if j := strings.Index(s[i+2:], "*/"); j >= 0 {
					i += 2 + j + 2
					continue
				}
				return b.String()
			}
		}
		if c == '"' || (javaOrCS && (c == '@' || c == '$')) {
			if end, ok := stringLiteralEnd(s, i, javaOrCS); ok {
				b.WriteByte('_')
				i = end
				continue
			}
			// Not a literal: an `@class` keyword-identifier or a bare `$`. Fall through.
		}
		if c == '\'' && javaOrCS {
			b.WriteByte('_')
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if s[i] == '\'' {
					i++
					break
				}
				i++
			}
			continue
		}
		if c == '`' && goExt {
			b.WriteByte('_')
			i++
			for i < n && s[i] != '`' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// javaEscapeLeads / csharpEscapeLeads are the characters that may legally follow a backslash inside
// a string or char literal. Octal escapes (`\0`-`\377`) are covered by the digits being present in
// the Java set; accepting a slightly wider grammar than the spec is deliberate, because the cost of
// the two error directions is not symmetric — see illegalEscapeReason.
var javaEscapeLeads = map[byte]bool{
	'b': true, 's': true, 't': true, 'n': true, 'f': true, 'r': true,
	'"': true, '\'': true, '\\': true, 'u': true,
	'0': true, '1': true, '2': true, '3': true, '4': true, '5': true, '6': true, '7': true,
}

var csharpEscapeLeads = map[byte]bool{
	'\'': true, '"': true, '\\': true, '0': true, 'a': true, 'b': true, 'f': true,
	'n': true, 'r': true, 't': true, 'v': true, 'u': true, 'U': true, 'x': true,
}

// IllegalEscapeReason is the FIX-PATH-ONLY escape gate. It is deliberately NOT part of
// SyntacticShellReason, and that separation is the whole point of this comment.
//
// The two write paths have opposite consequences for a rejection, which is a distinction the first
// version of this check got wrong and a run paid for. In the FIX path a rejected write leaves the
// previous version of the file on disk: the artifact survives, the diagnostic repeats, and the next
// round tries again — so refusing to write a body with an illegal escape costs one round and saves
// a containerised compile. In the GENERATE path there is no previous version. A rejection there
// means the file is never created at all, while the path stays in ArtifactPaths, so the evaluator
// later reports `fix_missing_required_context` for an artifact it can neither read nor repair.
//
// asqs-go run api-c3e4a6ea003d0f9b1aeb487b4a8faec6 is what that looks like: 12 artifacts planned, 3 of them
// (OwnerControllerE2EIT.java, OwnerTests.java, VetControllerE2EIT.java) generated successfully and
// then dropped by writeGeneratedFiles, leaving the fix round with 5 artifacts instead of 8 and no
// way to repair the missing three. An illegal escape is a one-line repair and precisely what the
// fix loop exists for; destroying the artifact to avoid it is a strictly worse trade.
//
// SyntacticShellReason keeps its own checks in both paths because they detect content that is
// unusable rather than broken — a markdown fence, a truncated body, no type declaration at all.
// There is nothing in those to repair.
func IllegalEscapeReason(path, content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		return illegalEscapeReason(content, ".java")
	case ".cs":
		return illegalEscapeReason(content, ".cs")
	}
	return ""
}

// EscapeRepair records one backslash the repair doubled.
type EscapeRepair struct {
	Line   int
	Escape string // the illegal sequence as written, e.g. `\d`
}

// RepairIllegalEscapes doubles every backslash that javac/roslyn would reject inside a STRING
// literal of a Java or C# file, and reports what it changed. Content is returned unchanged (with no
// repairs) for other languages, for files carrying constructs the scanner does not model (text
// blocks, verbatim strings) and for anything it cannot tokenize with confidence — the same bail
// rules as illegalEscapeReason, so a repaired file is one the gate would then accept.
//
// The repair is deterministic and total for strings: `\d`, `\w`, `\.`, `\/` and every other
// non-escape are regex or path notation the model forgot to double, and `\\d` is the only
// reading javac has for what was meant. Char literals are left alone: `'\d'` doubled is a
// two-character literal, which does not compile either, so that one stays a rejection.
//
// Why repair rather than reject: run api-5a67a414d4ba22496fcc23e1143076fa spent a containerised
// compile and two fixer rounds (24 minutes on a local model) on a single `\d` — the first fixer
// reply repeated the same escape and was refused. There is nothing for a model to decide here.
func RepairIllegalEscapes(path, content string) (string, []EscapeRepair) {
	if strings.TrimSpace(content) == "" {
		return content, nil
	}
	var leads map[byte]bool
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		leads = javaEscapeLeads
	case ".cs":
		leads = csharpEscapeLeads
	default:
		return content, nil
	}
	s := content
	isCS := strings.EqualFold(filepath.Ext(path), ".cs")
	var b strings.Builder
	var repairs []EscapeRepair
	i, n := 0, len(s)
	line := 1
	for i < n {
		// A literal this scanner does not model is copied through untouched: inside it a backslash
		// is a backslash, so there is nothing to repair and doubling one would corrupt the file.
		if isCS {
			if end, ok := csharpUnmodelledLiteralEnd(s, i); ok {
				line += strings.Count(s[i:end], "\n")
				b.WriteString(s[i:end])
				i = end
				continue
			}
		}
		c := s[i]
		switch {
		case c == '\n':
			line++
		case c == '/' && i+1 < n && s[i+1] == '/':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return content, nil
			}
			b.WriteString(s[i : i+j])
			i += j
			continue
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return content, nil
			}
			end := i + 2 + j + 2
			line += strings.Count(s[i:end], "\n")
			b.WriteString(s[i:end])
			i = end
			continue
		case c == '"' || c == '\'':
			quote := c
			start := i
			i++
			closed := false
			var lit strings.Builder
			lit.WriteByte(quote)
			for i < n {
				if s[i] == '\n' {
					return content, nil
				}
				if s[i] == '\\' {
					if i+1 >= n || s[i+1] == '\n' || s[i+1] == '\r' {
						return content, nil
					}
					esc := s[i+1]
					if !leads[esc] && quote == '"' {
						repairs = append(repairs, EscapeRepair{Line: line, Escape: `\` + string(esc)})
						lit.WriteString(`\\`)
						lit.WriteByte(esc)
						i += 2
						continue
					}
					lit.WriteByte(s[i])
					lit.WriteByte(esc)
					i += 2
					continue
				}
				lit.WriteByte(s[i])
				if s[i] == quote {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return content, nil
			}
			_ = start
			b.WriteString(lit.String())
			continue
		}
		b.WriteByte(c)
		i++
	}
	if len(repairs) == 0 {
		return content, nil
	}
	return b.String(), repairs
}

// DescribeEscapeRepairs renders repairs for a log line: `\d at line 55; \w at line 60`.
func DescribeEscapeRepairs(repairs []EscapeRepair) string {
	parts := make([]string, 0, len(repairs))
	for _, r := range repairs {
		parts = append(parts, fmt.Sprintf("%s at line %d", r.Escape, r.Line))
	}
	return strings.Join(parts, "; ")
}

// illegalEscapeReason reports a backslash escape that javac/roslyn will reject inside a string or
// char literal, or "" when the file is clean or cannot be tokenized with confidence.
//
// This closes the last gap in the pre-write gate. A fixer round rewrote OwnerControllerE2EIT.java —
// a file the diagnostic had not blamed — and returned a body containing an escape javac rejects
// (`illegal escape character` at 81:84). The gate passed it because stripStringsAndComments
// deliberately DISCARDS literal contents, so nothing here ever looked inside a string. The file
// reached disk, cost a full 22-second containerised compile to discover, and the round that tried
// to repair it is the round that ended the run.
//
// The two error directions cost very different amounts, and the implementation is asymmetric to
// match. A false negative costs one compile cycle — exactly what happens today. A false positive
// silently discards a CORRECT repair and burns a whole LLM round, which is strictly worse than the
// bug being fixed. So this bails out (returns "") on every construct it cannot tokenize exactly:
//
//   - Java text blocks and C# raw strings (`"""`), which stripStringsAndComments also skips;
//   - C# verbatim strings (`@"…"`, `$@"…"`, `@$"…"`), where a backslash is a literal backslash and
//     `C:\dir` is correct code. Both orderings are excluded: `@$"` does not contain the substring
//     `@"`, so testing for that alone would scan a verbatim literal under non-verbatim rules;
//   - any literal left unterminated at EOF or across a newline, which means the tokenizer lost
//     sync — including a trailing backslash, which javac reports as an unclosed literal rather
//     than as an illegal escape.
//
// Interpolated non-verbatim strings (`$"…"`) are NOT skipped: they process escapes under the normal
// rules, so the ordinary scan is correct for them.
func illegalEscapeReason(s, ext string) string {
	var leads map[byte]bool
	var lang string
	switch ext {
	case ".java":
		leads, lang = javaEscapeLeads, "Java"
	case ".cs":
		leads, lang = csharpEscapeLeads, "C#"
	default:
		return ""
	}
	i, n := 0, len(s)
	line := 1
	for i < n {
		// Skip the literal forms whose escape rules this scanner does not model, rather than
		// abandoning the whole file the moment one appears.
		if lang == "C#" {
			if end, ok := csharpUnmodelledLiteralEnd(s, i); ok {
				line += strings.Count(s[i:end], "\n")
				i = end
				continue
			}
		}
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
			continue
		case c == '/' && i+1 < n && s[i+1] == '/':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return ""
			}
			i += j // leave the newline for the counter above
			continue
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return ""
			}
			line += strings.Count(s[i:i+2+j+2], "\n")
			i += 2 + j + 2
			continue
		case c == '"' || c == '\'':
			quote := c
			i++
			closed := false
			for i < n {
				if s[i] == '\n' {
					// An unterminated literal at end of line means the tokenizer lost sync
					// (or the source is truncated); either way, stop guessing.
					return ""
				}
				if s[i] == '\\' {
					if i+1 >= n || s[i+1] == '\n' || s[i+1] == '\r' {
						return ""
					}
					esc := s[i+1]
					if !leads[esc] {
						return fmt.Sprintf(
							"illegal escape character %q in %s string literal at line %d; %s rejects this and the file will not compile",
							`\`+string(esc), lang, line, lang)
					}
					i += 2
					continue
				}
				if s[i] == quote {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return ""
			}
			continue
		}
		i++
	}
	return ""
}

// fixEmptyTestGateApplies reports whether the empty-test gate should judge a write to this path.
//
// The gate's subject is "a test that came back with no tests", and three cases are exactly that:
// a file this run generated (an artifact with no test bought nothing, whatever is on disk now), a
// path with no prior content (the fixer inventing an empty test file), and a file that DID declare
// tests before this round (the write is removing them).
//
// The case it must not judge is the fourth: a file that lives in a test project, has never declared
// a test, and never will — test SUPPORT. A validation run lost its whole fix loop to it. Three of
// four failing tests came from a broken SQLite fallback in CustomWebApplicationFactory.cs; the model
// located that file and returned an edit for it on three consecutive rounds; each was refused here
// as an "empty C# test file", each round was recorded as producing nothing usable, and the third
// refusal stopped the run with fixer_response_unusable — with the repair sitting in the response
// every time.
func fixEmptyTestGateApplies(rel string, opts EvalOptions, before map[string]string) bool {
	for _, a := range opts.ArtifactPaths {
		if normalizePathForFix(a) == rel {
			return true
		}
	}
	prior, ok := before[rel]
	if !ok || strings.TrimSpace(prior) == "" {
		return true
	}
	// It declared tests before, so this write is taking them away.
	return EmptyTestFileReason(rel, prior) == ""
}
