package evaluator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reFixLowValueTypeOfNotNull = regexp.MustCompile(`(?is)typeof\s*\(.*?\).*?Assert\.NotNull\s*\(`)
	reFixLowValueSelfSmoke     = regexp.MustCompile(`(?is)Assert\.NotNull\s*\(\s*new\s+[A-Za-z_][A-Za-z0-9_]*Tests?\s*\(`)
	reFixLowValueCSharpSkip    = regexp.MustCompile(`(?is)\[(?:Fact|Theory)\s*\(\s*Skip\s*=.*?\)\][\s\S]{0,160}?\{\s*(?://[^\n]*\n|\s)*\}`)
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
// the compiler is still the authoritative check): unknown extensions return "", TypeScript /
// JavaScript are intentionally excluded because template-literal interpolation (`${…}`) makes
// simple brace counting unsafe, and the Java/C# top-level-declaration check only fires when
// the file contains ZERO type declarations (a real file with extra top-level garbage BEFORE
// the class is caught by the brace/fence checks but not by the declaration regex). Returns
// "" for whitespace-only content so the caller can emit evaluator.fix_skip_empty instead.
//
// Supported languages: Java (.java), C# (.cs), Go (.go).
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
	}
	return ""
}

func javaSyntacticShellReason(s string) string {
	if strings.Contains(s, "```") {
		return "contains markdown code fence (```), LLM emitted fenced output instead of raw Java source"
	}
	stripped := stripStringsAndComments(s, ".java")
	if op, cl := strings.Count(stripped, "{"), strings.Count(stripped, "}"); op != cl {
		return fmt.Sprintf("unbalanced braces ({=%d, }=%d), Java source is truncated or mis-nested", op, cl)
	}
	if !reJavaTypeDecl.MatchString(stripped) {
		return "no class/interface/enum/record declaration, file will not parse as Java"
	}
	return ""
}

func csharpSyntacticShellReason(s string) string {
	if strings.Contains(s, "```") {
		return "contains markdown code fence (```), LLM emitted fenced output instead of raw C# source"
	}
	stripped := stripStringsAndComments(s, ".cs")
	if op, cl := strings.Count(stripped, "{"), strings.Count(stripped, "}"); op != cl {
		return fmt.Sprintf("unbalanced braces ({=%d, }=%d), C# source is truncated or mis-nested", op, cl)
	}
	if !reCSharpTypeDecl.MatchString(stripped) {
		return "no class/interface/struct/enum/record/delegate declaration, file will not parse as C#"
	}
	return ""
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

// stripStringsAndComments is a cheap approximate tokenizer that replaces the contents of string
// literals, char literals, and // / /* */ comments with a placeholder byte so downstream brace
// counting doesn't trip over legitimate "{"/"}" characters inside strings or comments. It does
// NOT handle Java text blocks (`"""…"""`), C# verbatim strings (`@"…"`), C# raw strings
// (`"""…"""`), or C# interpolated strings (`$"…{expr}…"`) — those are rare in generated test
// files and false-positives from them would at worst make us MORE conservative (we'd see a
// "}"-only tail and report unbalanced braces, which is exactly the failure mode we're trying
// to catch anyway). Supported extensions: `.java`, `.cs` (with // and /* */ comments plus
// double-quoted strings and single-quoted char literals), `.go` (adds backtick raw strings).
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
		if c == '"' {
			b.WriteByte('_')
			i++
			for i < n {
				if s[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
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
