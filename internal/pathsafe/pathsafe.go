// Package pathsafe contains the repo-root containment check shared by anything that accepts a path
// from an untrusted source — the Copilot permission gate and the model-facing read tools.
//
// It lives in its own package because there must be exactly one implementation. A second copy of a
// containment check is a second chance to get it subtly wrong, and the two consumers here are the
// places where getting it wrong means reading or writing outside the repository.
package pathsafe

import (
	"path/filepath"
	"strings"
)

// ContainedRelPath resolves candidate against root and returns the cleaned, slash-separated
// repo-relative path, reporting false when the candidate escapes root or is empty.
//
// An empty root has no directory to contain against, so rather than allowing everything it rejects
// anything absolute or upward-walking: the permissive branch must not be reachable by a traversal
// payload either.
func ContainedRelPath(candidate, root string) (string, bool) {
	c := strings.TrimSpace(candidate)
	if c == "" {
		return "", false
	}
	if root == "" {
		p := filepath.ToSlash(filepath.Clean(c))
		if filepath.IsAbs(c) || p == ".." || strings.HasPrefix(p, "../") {
			return "", false
		}
		return p, true
	}
	abs := c
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(filepath.Clean(root), abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
