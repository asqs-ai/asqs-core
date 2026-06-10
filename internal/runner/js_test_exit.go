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
