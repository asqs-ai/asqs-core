package layout

import (
	"os"
	"path/filepath"
	"strings"
)

// DedicatedRootDirCandidates are repo-root directory names that indicate a shared unit-test tree
// (used for C# and JS/TS when mirroring src/… into tests/…). First existing match wins in DetectDedicatedRoot.
var DedicatedRootDirCandidates = []string{
	"tests", "Tests", "test", "UnitTests", "FunctionalTests",
	"functional_tests", "unit_tests", "testing", "Test",
}

// DetectDedicatedRoot returns the actual directory name at repoAbs if one of DedicatedRootDirCandidates exists, else "".
func DetectDedicatedRoot(repoAbs string) string {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs == "" {
		return ""
	}
	ents, err := os.ReadDir(repoAbs)
	if err != nil {
		return ""
	}
	seen := make(map[string]bool)
	for _, e := range ents {
		if e.IsDir() {
			seen[e.Name()] = true
		}
	}
	for _, cand := range DedicatedRootDirCandidates {
		if seen[cand] {
			return cand
		}
	}
	return ""
}

func isDedicatedRootSegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return false
	}
	for _, c := range DedicatedRootDirCandidates {
		if strings.EqualFold(seg, c) {
			return true
		}
	}
	return false
}

// MirrorDirForTests returns the subdirectory layout under the dedicated tests root.
// Leading src/ or source/ (case-insensitive) is stripped.
func MirrorDirForTests(sourceDirRel string) string {
	d := filepath.ToSlash(strings.TrimSpace(sourceDirRel))
	if d == "." || d == "" {
		return ""
	}
	lower := strings.ToLower(d)
	for _, prefix := range []string{"src/", "source/"} {
		if strings.HasPrefix(lower, prefix) {
			d = d[len(prefix):]
			lower = strings.ToLower(d)
			break
		}
	}
	d = strings.Trim(d, "/")
	return d
}

func sourcePathCandidates(mirrorPath string, sourceFileName string) []string {
	if mirrorPath != "" {
		return []string{
			filepath.Join("src", mirrorPath, sourceFileName),
			filepath.Join("source", mirrorPath, sourceFileName),
			filepath.Join(mirrorPath, sourceFileName),
		}
	}
	return []string{
		filepath.Join("src", sourceFileName),
		filepath.Join("source", sourceFileName),
		sourceFileName,
	}
}

func firstExistingSourcePath(candidates []string, repoAbs string) string {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs != "" {
		for _, p := range candidates {
			full := filepath.Join(repoAbs, p)
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				return filepath.ToSlash(p)
			}
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return filepath.ToSlash(candidates[0])
}
