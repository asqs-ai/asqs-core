package dotnetproj

import (
	"path/filepath"
	"strings"
)

// WalkSkipDir reports whether a directory should be skipped when walking a .NET repository for
// project files.
//
// One list, because there were two and they disagreed: runner/profile skipped "test-results" but not
// "testresults", layout skipped "testresults" but not "test-results", and each walk therefore
// descended into build output the other pruned. Everything here is either version control, a
// dependency cache, or build output — never a place a hand-written .csproj lives.
//
// Callers looking for build OUTPUT (a coverage report under TestResults/, say) must not use this.
func WalkSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "node_modules", ".git", "bin", "obj", "packages", "dist", "target", "out",
		"build", "coverage", ".vs", ".vscode", "venv", "__pycache__", "vendor",
		"playwright-report", "test-results", "testresults", ".gradle", ".idea", ".next":
		return true
	default:
		return len(name) > 0 && name[0] == '.'
	}
}

// WalkDepth is the number of path segments between root and abs; 0 when they are the same
// directory. Used to bound recursive project discovery on large monorepos.
func WalkDepth(root, abs string) int {
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}
