package errout

import (
	"fmt"
	"regexp"
	"strings"
)

// Test-failure block extraction. Test logs are dominated by framework noise: a Spring Boot
// surefire run prints hundreds of INFO/startup lines before and between the failures. This
// extractor keeps only the failure-relevant blocks, in original order, and is shared by two
// consumers with the same need from opposite ends of the pipeline:
//
//   - the fixer's prompt gist (llmfix.errorLogGistForStep), where a head+tail cut of the raw log
//     used to spend its whole budget on startup noise while the assertions sat in the dropped
//     middle (run api-148358c668670fd95da8c4e65afa445a);
//   - the failed test step's audit/stderr summary, which was firstLines(out, 5) — in run
//     api-12aa1935d113c9ea8b50a516fd275660 that meant 767 chars of Spring INFO and not one of the
//     ten failures, so the post-mortem had to be reconstructed from surefire XML files.
//
// Moved here from llmfix because the second consumer (internal/runner) cannot import llmfix.

var (
	// surefire per-test failure markers and per-class summaries.
	surefireFailureMarker = regexp.MustCompile(`<<<\s*(FAILURE|ERROR)!`)
	// "Tests run: 5, Failures: 1, Errors: 0" — only interesting when something failed.
	testsRunSummaryLine = regexp.MustCompile(`(?i)(failures|errors):\s*[1-9]`)
	// "org.opentest4j.AssertionFailedError: ..." / "java.lang.IllegalStateException: ..." heads.
	exceptionHeadLine = regexp.MustCompile(`\b[A-Z]\w*(Exception|Error)\b\s*:`)
)

// maxFramesPerFailureBlock bounds stack frames kept per failure block: the top frames carry the
// test's own file:line; the rest is framework plumbing.
const maxFramesPerFailureBlock = 8

// maxContinuationLinesPerBlock bounds non-frame continuation lines (assertion diffs, expected/actual
// dumps) per block so one enormous object diff cannot consume the whole excerpt.
const maxContinuationLinesPerBlock = 20

func testFailureMarkerLine(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	if surefireFailureMarker.MatchString(s) || testsRunSummaryLine.MatchString(s) {
		return true
	}
	upper := strings.ToUpper(s)
	if strings.HasPrefix(upper, "FAIL ") || strings.HasSuffix(upper, " FAILED") {
		return true
	}
	// Jest/vitest failure bullets.
	if strings.HasPrefix(s, "● ") || strings.Contains(s, "✗ ") || strings.Contains(s, "✕ ") || strings.HasPrefix(s, "❯ ") {
		return true
	}
	if strings.Contains(s, "[ERROR]") {
		return true
	}
	if strings.Contains(s, "AssertionError") || strings.Contains(s, "AssertionFailedError") ||
		strings.Contains(s, "Expected:") || strings.Contains(s, "Received:") ||
		strings.Contains(s, "expected:") || strings.Contains(s, "but was") {
		return true
	}
	if exceptionHeadLine.MatchString(s) {
		return true
	}
	return false
}

// testFailureStackFrameLine matches "at pkg.Class.method(File.java:12)"-style frames (Java and JS).
func testFailureStackFrameLine(s string) bool {
	if !strings.HasPrefix(s, "at ") {
		return false
	}
	return strings.Contains(s, ".java:") || strings.Contains(s, ".kt:") ||
		strings.Contains(s, ".ts:") || strings.Contains(s, ".tsx:") ||
		strings.Contains(s, ".js:") || strings.Contains(s, "Unknown Source")
}

func testFailureContinuationLine(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	if testFailureStackFrameLine(s) {
		return true
	}
	if strings.HasPrefix(s, "Caused by:") || strings.HasPrefix(s, "...") {
		return true
	}
	// Indented content directly under a marker (assertion diffs, expected/actual dumps).
	return line != s
}

// ExtractTestFailureBlocks returns only the failure-relevant blocks of a test log, in original
// order, with "[...]" separators where non-failure lines were dropped. Returns "" when the log
// contains no recognisable failure marker, in which case the caller must fall back to its plain
// head/gist behaviour rather than presenting an empty excerpt.
func ExtractTestFailureBlocks(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	included := 0
	lastEmitted := -2 // line index of the previously emitted line; -2 so line 0 never looks adjacent
	emit := func(i int) {
		if i != lastEmitted+1 && len(out) > 0 {
			out = append(out, "[...]")
		}
		out = append(out, lines[i])
		lastEmitted = i
		included++
	}
	i := 0
	for i < len(lines) {
		if !testFailureMarkerLine(lines[i]) {
			i++
			continue
		}
		emit(i)
		i++
		frames, cont := 0, 0
		for i < len(lines) && testFailureContinuationLine(lines[i]) {
			if testFailureStackFrameLine(strings.TrimSpace(lines[i])) {
				if frames < maxFramesPerFailureBlock {
					emit(i)
				}
				frames++
			} else {
				if cont < maxContinuationLinesPerBlock {
					emit(i)
				}
				cont++
			}
			i++
		}
	}
	if included == 0 {
		return ""
	}
	header := fmt.Sprintf("[test-failure excerpt: %d of %d log lines; framework/INFO noise omitted]", included, len(lines))
	return header + "\n" + strings.Join(out, "\n")
}
