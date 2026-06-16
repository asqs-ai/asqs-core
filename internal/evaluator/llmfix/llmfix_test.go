package llmfix

import "testing"

// TestRankDependencyPaths_ErrorReferencedFirst: files cited by the error log are emitted first
// (so they survive the budget), then the rest alphabetically; already-emitted artifacts are skipped.
func TestRankDependencyPaths_ErrorReferencedFirst(t *testing.T) {
	files := map[string]string{
		"src/a/Alpha.java":      "",
		"src/b/Beta.java":       "",
		"src/c/Referenced.java": "",
		"src/d/Artifact.java":   "",
	}
	emitted := map[string]bool{"src/d/Artifact.java": true} // already emitted as the failing artifact
	lineByPath := map[string][]int{"src/c/Referenced.java": {42}}

	got := rankDependencyPaths(files, emitted, lineByPath)
	want := []string{"src/c/Referenced.java", "src/a/Alpha.java", "src/b/Beta.java"}
	if len(got) != len(want) {
		t.Fatalf("rankDependencyPaths len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rankDependencyPaths[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
