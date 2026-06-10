package dotnetproj

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NearestCsprojRel returns the repo-relative path to the closest .csproj when walking up from sourceFileRel's directory.
// sourceFileRel uses slash separators relative to repo root (e.g. "src/App/Foo.cs"). If sourceFileRel is empty,
// RootCsprojRel is used. If no project is found, ok is false.
func NearestCsprojRel(repoRoot, sourceFileRel string) (rel string, ok bool) {
	repoRoot = filepath.Clean(repoRoot)
	sourceFileRel = filepath.ToSlash(strings.TrimSpace(sourceFileRel))
	if sourceFileRel == "" {
		return RootCsprojRel(repoRoot)
	}
	dir := filepath.ToSlash(filepath.Dir(sourceFileRel))
	for {
		if dir == "." || dir == "" {
			break
		}
		fullDir := filepath.Join(repoRoot, filepath.FromSlash(dir))
		if rel, ok := firstCsprojInDirRel(dir, fullDir); ok {
			return rel, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return RootCsprojRel(repoRoot)
}

// RootCsprojRel returns the first .csproj (lexicographic by basename) in repoRoot only (non-recursive).
func RootCsprojRel(repoRoot string) (rel string, ok bool) {
	return firstCsprojInDirRel(".", repoRoot)
}

func firstCsprojInDirRel(relDir, absDir string) (string, bool) {
	ents, err := os.ReadDir(absDir)
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(strings.ToLower(n), ".csproj") {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	joined := filepath.ToSlash(filepath.Join(relDir, names[0]))
	if relDir == "." {
		joined = filepath.ToSlash(names[0])
	}
	return joined, true
}
