package evaluator

import (
	"fmt"
	"path/filepath"
	"strings"
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
	if !strings.HasSuffix(base, ".java") {
		return ""
	}
	beforeImports := map[string]bool{}
	for _, m := range javaImportSimpleNames(before) {
		beforeImports[m] = true
	}
	var unused []string
	for _, name := range javaImportSimpleNames(after) {
		if beforeImports[name] {
			continue // pre-existing; not this write's doing
		}
		if name == "*" {
			continue
		}
		// Strip the import lines themselves before looking for a use.
		body := stripJavaImportLines(after)
		if !strings.Contains(body, name) {
			unused = append(unused, name)
		}
	}
	if len(unused) == 0 {
		return ""
	}
	return "added import(s) nothing references: " + strings.Join(unused, ", ")
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

// testFilesWithNoRunnableTests returns the test files a jest run refused to execute because it
// registered no test in them, keyed by normalized repo-relative path.
//
// jest reports that as
//
//	FAIL src/app/features/checkout/checkout.component.test.ts
//	  ● Test suite failed to run
//
//	    Your test suite must contain at least one test.
//
// and the ONLY path in that block is the FAIL header — no `path:line`. The coverage gate below
// counts `it(`/`test(` occurrences statically, and for such a file the "before" count is a fiction:
// the six matches it found in checkout.component.test.ts (run of 2026-09-03) were never run, so a
// rewrite carrying five real tests is not a regression, it is the repair. Rejecting it (which the
// gate did, in the only round that file was writable) left the file to be discarded unexamined.
func testFilesWithNoRunnableTests(output string) map[string]bool {
	out := map[string]bool{}
	current := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
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
