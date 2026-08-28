package runner

import "testing"

func TestJsTestOutputSummaryShowsZeroFailures(t *testing.T) {
	t.Run("jest_open_handles_style_all_passed", func(t *testing.T) {
		out := `
PASS src/a.test.ts
  ✓ one

Test Suites: 1 passed, 1 total
Tests:       3 passed, 3 total
Snapshots:   0 total
Time:        0.5 s
Jest did not exit one second after the test run has completed.
This usually means that there are asynchronous operations that weren't stopped in your tests.
`
		if !jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected true for all-passed summary despite open-handles message")
		}
	})
	t.Run("jest_real_failure", func(t *testing.T) {
		out := `
FAIL src/bad.test.ts
  ✕ oops

Test Suites: 1 failed, 1 total
Tests:       1 failed, 1 total
`
		if jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected false when summary reports failed suites")
		}
	})
	t.Run("jest_tests_line_failed", func(t *testing.T) {
		out := `
Test Suites: 1 failed, 2 passed, 3 total
Tests:       2 failed, 8 passed, 10 total
`
		if jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected false when Tests line reports failures")
		}
	})
	t.Run("jest_zero_failed_explicit", func(t *testing.T) {
		out := `
Test Suites: 2 passed, 2 total
Tests:       0 failed, 5 passed, 5 total
`
		if !jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected true for 0 failed, N passed")
		}
	})
	t.Run("vitest_passed", func(t *testing.T) {
		out := `
 ✓ src/a.test.ts (2 tests) 12ms

 Test Files  1 passed (1)
      Tests  2 passed (2)
`
		if !jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected true for vitest passed table")
		}
	})
	t.Run("vitest_failed", func(t *testing.T) {
		out := `
 Test Files  1 failed | 1 passed (2)
      Tests  1 failed | 3 passed (4)
`
		if jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("expected false for vitest failures")
		}
	})
	t.Run("empty_or_noise", func(t *testing.T) {
		if jsTestOutputSummaryShowsZeroFailures("") {
			t.Fatal("empty -> false")
		}
		if jsTestOutputSummaryShowsZeroFailures("npm ERR! code ELIFECYCLE") {
			t.Fatal("npm error without jest summary -> false")
		}
	})
	t.Run("jest_coverage_threshold_not_met", func(t *testing.T) {
		out := `
Test Suites: 1 passed, 1 total
Tests:       2 passed, 2 total
Jest: "global" coverage threshold for statements (80%) not met: 45.2%
`
		if jsTestOutputSummaryShowsZeroFailures(out) {
			t.Fatal("coverage threshold failure must not be treated as all-passed")
		}
	})
}
