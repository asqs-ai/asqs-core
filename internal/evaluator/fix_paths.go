package evaluator

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveReadableRepoFile returns a repo-relative normalized path if a regular file exists under
// repoRoot, trying monoRepoWorkspace/prefix when the direct path is missing.
func ResolveReadableRepoFile(repoRoot, rel, monoWorkspace string) (norm string, ok bool) {
	repoRoot = filepath.Clean(repoRoot)
	norm = NormalizeRepoRelPath(rel)
	if norm == "" {
		return "", false
	}
	mw := strings.Trim(strings.TrimSpace(filepath.ToSlash(monoWorkspace)), "/")
	var candidates []string
	candidates = append(candidates, norm)
	if mw != "" {
		candidates = append(candidates, mw+"/"+norm)
	}
	for _, c := range candidates {
		c = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(c)), "/")
		full := filepath.Join(repoRoot, filepath.FromSlash(c))
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		return c, true
	}
	return norm, false
}
