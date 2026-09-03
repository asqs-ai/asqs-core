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

// A coloured vitest table must still read as a failure: the exit-code override is only for runs
// whose summary proves zero failures, and colour codes must not turn a red table into a green one
// or vice versa.
func TestJsTestOutputSummaryShowsZeroFailures_colouredVitestFailureStaysAFailure(t *testing.T) {
	out := "\x1b[2m Test Files \x1b[22m \x1b[1m\x1b[31m5 failed\x1b[39m\x1b[22m\x1b[2m | \x1b[22m\x1b[1m\x1b[32m2 passed\x1b[39m\x1b[22m\x1b[90m (7)\x1b[39m\n" +
		"\x1b[2m      Tests \x1b[22m \x1b[1m\x1b[31m21 failed\x1b[39m\x1b[22m\x1b[2m | \x1b[22m\x1b[1m\x1b[32m13 passed\x1b[39m\x1b[22m\x1b[90m (34)\x1b[39m\n"
	if jsTestOutputSummaryShowsZeroFailures(out) {
		t.Fatal("a coloured '21 failed' table must not be read as zero failures")
	}
}

// "No test files found" is neither a pass with an odd exit code nor a failure: nothing ran. The
// runner reports it distinctly (evaluator.NoTestFilesSuffix) and the evaluator decides.
func TestJsTestOutputReportsNoTestFiles(t *testing.T) {
	cases := map[string]struct {
		out  string
		want bool
	}{
		"vitest":                            {"\n RUN  v4.1.11 /workspace\n\nNo test files found, exiting with code 1\n\ninclude: **/__tests__/**/*.{test,spec}.{js,jsx,ts,tsx}, **/*.{test,spec}.{js,jsx,ts,tsx}\nexclude:  **/node_modules/**, **/dist/**, e2e/**, cypress/**\n", true},
		"jest":                              {"No tests found, exiting with code 1\nRun with `--passWithNoTests` to exit with code 0\nIn /workspace\n  12 files checked.\n", true},
		"vitest_with_failures_is_a_failure": {"No test files found in some dir\n Test Files  1 failed (1)\n Tests  2 failed | 1 passed (3)\n", false},
		"ordinary_pass":                     {" Test Files  2 passed (2)\n Tests  9 passed (9)\n", false},
		"empty":                             {"", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := jsTestOutputReportsNoTestFiles(c.out); got != c.want {
				t.Fatalf("jsTestOutputReportsNoTestFiles = %v, want %v for:\n%s", got, c.want, c.out)
			}
		})
	}
}
