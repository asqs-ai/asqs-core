package runner

import (
	"regexp"
	"strings"
)

var (
	// Jest: "Test Suites: 1 failed, 1 total" or "Tests: 2 failed, 8 passed, 10 total"
	jestTestSuitesFailed = regexp.MustCompile(`(?i)Test Suites:\s*[^\n]*\b([1-9]\d*)\s+failed`)
	jestTestsFailed      = regexp.MustCompile(`(?i)Tests:\s*[^\n]*\b([1-9]\d*)\s+failed`)
	// Vitest table: "Tests  2 failed | 3 passed (5)" or "Test Files  1 failed (2)"
	vitestTestsFailed = regexp.MustCompile(`(?m)^\s*Tests\s+[1-9]\d*\s+failed`)
	vitestFilesFailed = regexp.MustCompile(`(?m)^\s*Test Files\s+[1-9]\d*\s+failed`)
)

// jsTestOutputSummaryShowsZeroFailures is true when Jest/Vitest summary lines report no failing suites/tests.
// Jest often exits non-zero while still printing "Tests: N passed, N total" (open handles, "Jest did not exit",
// --forceExit edge cases, coverage reporters). The evaluator should not treat that as a failing test step.
func jsTestOutputSummaryShowsZeroFailures(out string) bool {
	if strings.TrimSpace(out) == "" {
		return false
	}
	low := strings.ToLower(out)
	// Jest exits 1 when coverage thresholds fail while all tests passed; do not treat as a green run.
	if strings.Contains(low, "coverage threshold") && strings.Contains(low, "not met") {
		return false
	}
	if jestTestSuitesFailed.MatchString(out) || jestTestsFailed.MatchString(out) {
		return false
	}
	if vitestTestsFailed.MatchString(out) || vitestFilesFailed.MatchString(out) {
		return false
	}
	// Jest default reporter (completed run)
	if strings.Contains(out, "Test Suites:") && strings.Contains(out, "passed") && strings.Contains(out, "total") {
		return true
	}
	if strings.Contains(out, "Tests:") && strings.Contains(out, "passed") && strings.Contains(out, "total") {
		return true
	}
	// Vitest default table
	if vitestTestsPassedLine.MatchString(out) {
		return true
	}
	return false
}

var vitestTestsPassedLine = regexp.MustCompile(`(?m)^\s*Tests\s+\d+\s+passed`)

// jsNoTestFilesLine matches the message vitest and jest print when their include pattern matched
// no file at all. Both exit 1 in that case; neither ran a single test.
var jsNoTestFilesLine = regexp.MustCompile(`(?im)^\s*No (?:test files|tests) found`)

// jsTestOutputReportsNoTestFiles is true when a JS test runner exited non-zero only because it
// found no test files: vitest's "No test files found, exiting with code 1" and jest's "No tests
// found, exiting with code 1". Distinct from jsTestOutputSummaryShowsZeroFailures, which needs a
// summary table proving tests ran and passed; here nothing ran.
//
// The runner cannot know whether an empty tree is fine (only E2E specs survived a discard) or a
// misconfiguration (generated unit tests exist but the include glob misses them), so it reports
// the step as passed with evaluator.NoTestFilesSuffix and the evaluator decides
// (overrideNoTestFilesPass). Run api-72dad6bb281cacee338f43c48432a780 spent three repair rounds
// on this exact exit code after every unit artifact had been discarded.
func jsTestOutputReportsNoTestFiles(out string) bool {
	if strings.TrimSpace(out) == "" {
		return false
	}
	if jestTestSuitesFailed.MatchString(out) || jestTestsFailed.MatchString(out) ||
		vitestTestsFailed.MatchString(out) || vitestFilesFailed.MatchString(out) {
		return false
	}
	return jsNoTestFilesLine.MatchString(out)
}
