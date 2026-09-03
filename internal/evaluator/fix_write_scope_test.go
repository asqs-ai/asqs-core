package evaluator

import (
	"os"
	"path/filepath"
	"testing"
)

// Test-step counterpart of TestCompileErrorTouchesWritableScope: the gate fires only on output
// that names nothing writable, and both citation shapes (runner summary and path:line) count.
func TestTestFailureTouchesWritableScope(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"src/lib/validation.test.ts", "src/lib/validation.ts", "e2e/routes/home.spec.tsx"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultEvalOptions(dir, "typescript")
	opts.ArtifactPaths = []string{"src/lib/validation.test.ts", "e2e/routes/home.spec.tsx"}
	cases := map[string]struct {
		output string
		want   bool
	}{
		"empty_output_is_in_scope":          {"", true},
		"vitest_fail_line_names_artifact":   {" FAIL  src/lib/validation.test.ts > parsePositiveInt\nAssertionError: expected 3 to be null", true},
		"path_line_citation_names_artifact": {"Error: boom\n ❯ src/lib/validation.test.ts:27:38", true},
		"no_test_files_names_nothing":       {"No test files found, exiting with code 1\ninclude: **/*.test.ts\nexclude: e2e/**", false},
		// A location in production code is still the ordinary repair case (the generated test
		// drove the code there); scope narrowing decides what is writable, not this gate.
		"production_location_is_in_scope": {"TypeError: x is not a function\n    at parse (src/lib/validation.ts:4:3)", true},
		"runner_crash_without_location":   {"Error: Vitest failed to start: worker exited unexpectedly", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := testFailureTouchesWritableScope(c.output, opts, opts.ArtifactPaths); got != c.want {
				t.Fatalf("testFailureTouchesWritableScope(%q) = %v, want %v", c.output, got, c.want)
			}
		})
	}
	t.Run("no_writable_paths_at_all", func(t *testing.T) {
		// A different repo root: the named file is not on disk there, so it cannot be adopted
		// either, and the writable set is genuinely empty.
		empty := DefaultEvalOptions(t.TempDir(), "typescript")
		if testFailureTouchesWritableScope(" FAIL  src/lib/validation.test.ts", empty, nil) {
			t.Fatal("with nothing writable the gate must fire")
		}
	})
}
