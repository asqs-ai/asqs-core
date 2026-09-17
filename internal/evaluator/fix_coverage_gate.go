package evaluator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errloc"
)

// Coverage-preserving gate for fixer writes.
//
// The fixer resolves compile errors by deleting the tests. The complete net effect of a 54-minute
// run on WelcomeControllerTest.java was four now-unused imports and this:
//
//	+	// This class is not meant to be used for E2E tests with Playwright,
//	+	// so we'll skip the Playwright-based test methods here.
//
// The generated E2E tests were removed and the round was recorded as a successful fix. llmfix's
// system prompt already forbids exactly this — "Never return empty file content for an artifact,
// delete all test methods, or replace the file with only using directives and namespace comments"
// — but nothing verified it after the write, so the instruction was advisory.
//
// The gate is deliberately narrow: it fires only when a fixer write REDUCES the number of test
// methods. Renames, rewrites, added tests and reordering all pass. It runs only on fixer writes,
// never on generation writes, because an extend-existing merge legitimately adds methods and the
// dropDuplicateMembers path legitimately removes already-defined ones.

// countTestMethods returns how many test methods `content` declares for the language implied by
// `path`, and whether the language is one we can count at all.
//
// Reuses the marker regexes behind EmptyTestFileReason rather than introducing a second dialect of
// the same knowledge: a gate that disagrees with the emptiness check about what a test is would
// produce contradictory verdicts on the same file.
func countTestMethods(path, content string) (n int, known bool) {
	s := strings.TrimSpace(content)
	if s == "" {
		return 0, false
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".java"):
		return len(reEmptyTestMarkerJava.FindAllString(s, -1)), true
	case strings.HasSuffix(base, ".cs"):
		return len(reEmptyTestMarkerCSharp.FindAllString(s, -1)), true
	case strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx") ||
		strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".jsx") ||
		strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs"):
		return len(reEmptyTestMarkerJS.FindAllString(s, -1)), true
	case strings.HasSuffix(base, "_test.go"):
		return len(reEmptyTestMarkerGo.FindAllString(s, -1)), true
	}
	return 0, false
}

// coverageRegressionReason returns a non-empty reason when applying `after` over `before` would
// remove test coverage from `path`.
//
// Returns "" when the language is not countable, when `before` is empty (a fresh file cannot
// regress), or when the count holds or grows. The comparison is on counts rather than on identity
// of method names on purpose: a fixer that renames a test to something clearer is doing its job,
// and matching names would reject it.
func coverageRegressionReason(path, before, after string) string {
	beforeN, known := countTestMethods(path, before)
	if !known || beforeN == 0 {
		return ""
	}
	afterN, _ := countTestMethods(path, after)
	if afterN >= beforeN {
		return ""
	}
	return fmt.Sprintf("test method count would drop from %d to %d", beforeN, afterN)
}

// unusedImportResidueReason reports imports the fix ADDED that nothing in the new content uses.
//
// This is the visible signature of a deletion-shaped "fix": the WelcomeControllerTest round added
// Playwright imports and then removed the Playwright tests, leaving four imports referencing types
// the file no longer mentions. On its own an unused import is a warning, not an error — so this is
// reported and audited but, unlike the coverage check, does NOT block the write. Blocking on it
// would reject legitimate fixes whose reference lives in a form this simple scan cannot see
// (fully-qualified usage, annotations processed by name, string-based reflection).
func unusedImportResidueReason(path, before, after string) string {
	base := strings.ToLower(filepath.Base(path))
	simpleNames, stripLines := javaImportSimpleNames, stripJavaImportLines
	switch {
	case strings.HasSuffix(base, ".java"):
	case strings.HasSuffix(base, ".cs"):
		simpleNames, stripLines = csharpUsingSimpleNames, stripCSharpUsingLines
	default:
		return ""
	}
	beforeImports := map[string]bool{}
	for _, m := range simpleNames(before) {
		beforeImports[m] = true
	}
	var unused []string
	for _, name := range simpleNames(after) {
		if beforeImports[name] {
			continue // pre-existing; not this write's doing
		}
		if name == "*" {
			continue
		}
		// Strip the import lines themselves before looking for a use.
		body := stripLines(after)
		if !strings.Contains(body, name) {
			unused = append(unused, name)
		}
	}
	if len(unused) == 0 {
		return ""
	}
	return "added import(s) nothing references: " + strings.Join(unused, ", ")
}

// csharpUsingSimpleNames returns the identifier each using DIRECTIVE in src puts in scope — which,
// in C#, only ONE form actually has.
//
// Java's rule is "take the last segment of the import and look for it in the body", and it works
// because `import a.b.C;` imports the TYPE C. Neither C# form behaves that way:
//
//	using Moq;                   // imports a NAMESPACE; a test writing `new Mock<IRepo>()`
//	                             // never contains the word "Moq"
//	using static Xunit.Assert;   // imports Assert's MEMBERS; the body writes `True(...)`,
//	                             // not `Assert`
//
// Applying Java's rule to either would report a correct file's imports as dead residue, and this
// advisory goes straight into the fixer's prompt. Only the alias form names an identifier the body
// is obliged to write:
//
//	using Sut = Shop.Core.Basket;   // the body must say `Sut`
//
// So that is the only form judged here. The rest is left to the compiler, which reports an
// unnecessary directive as CS8019 and is the only thing that can actually know.
func csharpUsingSimpleNames(src string) []string {
	var out []string
	for _, ln := range strings.Split(src, "\n") {
		s := strings.TrimSpace(ln)
		s = strings.TrimPrefix(s, "global ")
		if !strings.HasPrefix(s, "using ") || !strings.HasSuffix(s, ";") || strings.Contains(s, "(") {
			continue
		}
		s = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, "using ")), ";")
		if strings.HasPrefix(s, "static ") {
			continue
		}
		if i := strings.Index(s, "="); i > 0 {
			if alias := strings.TrimSpace(s[:i]); alias != "" {
				out = append(out, alias)
			}
		}
	}
	return out
}

// stripCSharpUsingLines blanks the using directives so a name is not found in its own import.
func stripCSharpUsingLines(src string) string {
	var b strings.Builder
	for _, ln := range strings.Split(src, "\n") {
		s := strings.TrimSpace(ln)
		if strings.HasPrefix(s, "using ") || strings.HasPrefix(s, "global using ") {
			if strings.HasSuffix(s, ";") && !strings.Contains(s, "(") {
				b.WriteString("\n")
				continue
			}
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

// javaImportSimpleNames returns the trailing identifier of every import in src.
func javaImportSimpleNames(src string) []string {
	var out []string
	for _, ln := range strings.Split(src, "\n") {
		s := strings.TrimSpace(ln)
		if !strings.HasPrefix(s, "import ") {
			continue
		}
		s = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, "import ")), ";")
		s = strings.TrimSpace(strings.TrimPrefix(s, "static "))
		if i := strings.LastIndex(s, "."); i >= 0 {
			s = s[i+1:]
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stripJavaImportLines(src string) string {
	var b strings.Builder
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "import ") {
			continue
		}
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

// vitestZeroTestFileRE matches vitest's per-file summary line for a suite it executed no test
// from:
//
//	❯ src/app/AppLayout.test.tsx (0 test)
//
// Anchored on the parenthesised count rather than on the `❯` marker, which vitest also prints on
// stack frames. A suite that ran reads `(4 tests | 4 failed)` and cannot match.
var vitestZeroTestFileRE = regexp.MustCompile(`(?:^|\s)(\S+)\s+\(0 tests?\)`)

// testFilesWithNoRunnableTests returns the test files the runner executed no test from, keyed by
// normalized repo-relative path.
//
// The coverage gate below counts `it(`/`test(` occurrences statically, and for such a file the
// "before" count is a fiction: nothing in it ran, so a rewrite carrying fewer real tests is not a
// regression, it is the repair. Rejecting it leaves the file to be discarded unexamined.
//
// Two dialects, because the runners disagree about how to say it and the gate is language-blind:
//
//   - jest prints a block whose ONLY path is the FAIL header — no `path:line`:
//
//     FAIL src/app/features/checkout/checkout.component.test.ts
//     ● Test suite failed to run
//
//     Your test suite must contain at least one test.
//
//     The six matches the gate found in checkout.component.test.ts (run of 2026-09-03) were never
//     run, and it was rejected in the only round that file was writable.
//
//   - vitest never prints that sentence. It scores the file `(0 test)` instead, which also covers
//     the case jest has no equivalent for: a suite that failed to COLLECT. In run
//     an asqs-go React run src/app/AppLayout.test.tsx died on
//     `ReferenceError: src is not defined` before any test registered, so vitest reported
//     `(0 test)` while the file still statically declared six. Understanding only jest's spelling,
//     this returned an empty map on every vitest project, the waiver could not fire, and the gate
//     refused the same 6 → 5 repair on six consecutive rounds across 91 minutes.
//
// ANSI is stripped first: both runners colour these lines, and both branches match on literal
// prefixes that an escape code would hide.
func testFilesWithNoRunnableTests(output string) map[string]bool {
	out := map[string]bool{}
	current := ""
	for _, raw := range strings.Split(errloc.StripANSI(output), "\n") {
		line := strings.TrimSpace(raw)
		if m := vitestZeroTestFileRE.FindStringSubmatch(line); m != nil {
			out[normalizePathForFix(m[1])] = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "FAIL "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "FAIL "))
			// jest may append timing: "FAIL path (1.2 s)".
			if i := strings.Index(current, " ("); i > 0 {
				current = strings.TrimSpace(current[:i])
			}
		case strings.HasPrefix(line, "PASS "):
			current = ""
		case current != "" && strings.Contains(line, "Your test suite must contain at least one test"):
			out[normalizePathForFix(current)] = true
		}
	}
	return out
}
