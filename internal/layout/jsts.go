package layout

import (
	"os"
	"path/filepath"
	"strings"
)

// jstsSourceDirLooksLikeTestTree is true when the source file already lives under a test-only layout
// (skip mirroring into repo-root tests/ to avoid tests/tests/…).
func jstsSourceDirLooksLikeTestTree(dirRel string) bool {
	dirRel = filepath.ToSlash(strings.TrimSpace(dirRel))
	if dirRel == "" || dirRel == "." {
		return false
	}
	low := strings.ToLower(dirRel)
	segs := strings.Split(dirRel, "/")
	first := segs[0]
	for _, c := range DedicatedRootDirCandidates {
		if strings.EqualFold(first, c) {
			return true
		}
	}
	if strings.Contains(dirRel, "/__tests__/") || strings.HasSuffix(dirRel, "/__tests__") {
		return true
	}
	if strings.Contains(low, "/e2e/") || strings.HasPrefix(low, "e2e/") {
		return true
	}
	if strings.Contains(low, "/cypress/") || strings.HasPrefix(low, "cypress/") {
		return true
	}
	return false
}

// SuggestedJSTSUnitTestPath returns the repo-relative path for a new unit test file next to the source
// or under a dedicated root-level tests directory (mirrored like C#). testFramework: "jasmine" => .spec., else .test.
func SuggestedJSTSUnitTestPath(sourceFileRel, repoAbs, testFramework string) string {
	sourceFileRel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(sourceFileRel), "/"))
	if sourceFileRel == "" {
		return ""
	}
	base := filepath.Base(sourceFileRel)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	dir := filepath.Dir(sourceFileRel)
	suffix := ".test"
	if strings.EqualFold(strings.TrimSpace(testFramework), "jasmine") {
		suffix = ".spec"
	}
	testBase := name + suffix + ext

	if jstsSourceDirLooksLikeTestTree(dir) {
		return filepath.Join(dir, testBase)
	}

	root := DetectDedicatedRoot(repoAbs)
	if root == "" {
		return filepath.Join(dir, testBase)
	}
	mir := MirrorDirForTests(dir)
	if mir == "" {
		return filepath.Join(root, testBase)
	}
	return filepath.Join(root, filepath.FromSlash(mir), testBase)
}

// sourceNameFromJSTSTestBase strips a .test or .spec suffix from fileName and returns the likely
// production source filename. Both suffixes are always accepted so a jest repo can still resolve a
// stray x.spec.ts (and vice-versa for jasmine). The testFramework argument is used only to pick the
// ordering when a caller wants a single answer; SourcePathFromJSTSTestFile uses it with a disk check
// so the actual on-disk file wins regardless of framework.
func sourceNameFromJSTSTestBase(fileName, testFramework string) (sourceName string, ok bool) {
	ext := filepath.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)
	fw := strings.ToLower(strings.TrimSpace(testFramework))

	tryTest := func() (string, bool) {
		if strings.HasSuffix(stem, ".test") && len(stem) > 5 {
			return strings.TrimSuffix(stem, ".test") + ext, true
		}
		return "", false
	}
	trySpec := func() (string, bool) {
		if strings.HasSuffix(stem, ".spec") && len(stem) > 5 {
			return strings.TrimSuffix(stem, ".spec") + ext, true
		}
		return "", false
	}

	switch fw {
	case "jasmine":
		if s, ok := trySpec(); ok {
			return s, true
		}
		if s, ok := tryTest(); ok {
			return s, true
		}
	default:
		if s, ok := tryTest(); ok {
			return s, true
		}
		if s, ok := trySpec(); ok {
			return s, true
		}
	}
	return "", false
}

// sourceNameCandidatesFromJSTSTestBase returns every plausible source filename for a JS/TS test
// base, regardless of testFramework. Used by SourcePathFromJSTSTestFile so the on-disk check can pick
// whichever suffix the repo actually uses (jest repo with a stray x.spec.ts still resolves to x.ts).
func sourceNameCandidatesFromJSTSTestBase(fileName string) []string {
	ext := filepath.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)
	var out []string
	for _, suf := range []string{".test", ".spec"} {
		if strings.HasSuffix(stem, suf) && len(stem) > len(suf) {
			out = append(out, strings.TrimSuffix(stem, suf)+ext)
		}
	}
	return out
}

// SourcePathFromJSTSTestFile maps tests/…/foo.test.ts (or .spec for Jasmine) back to a likely source path.
// testFramework should match the repo’s unit runner (e.g. jest vs jasmine). Returns "" if not under a dedicated root or basename is not a unit test pattern.
func SourcePathFromJSTSTestFile(testFileRel, repoAbs, testFramework string) string {
	testFileRel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(testFileRel), "/"))
	if testFileRel == "" {
		return ""
	}
	segs := strings.Split(testFileRel, "/")
	if len(segs) < 2 {
		return ""
	}
	if !isDedicatedRootSegment(segs[0]) {
		return ""
	}
	fileName := segs[len(segs)-1]
	names := sourceNameCandidatesFromJSTSTestBase(fileName)
	if len(names) == 0 {
		return ""
	}
	var mirrorPath string
	if len(segs) > 2 {
		mirrorPath = filepath.FromSlash(strings.Join(segs[1:len(segs)-1], "/"))
	}
	// On-disk preferred: scan every (suffix, layout candidate) tuple, first hit wins.
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs != "" {
		for _, n := range names {
			for _, p := range sourcePathCandidates(mirrorPath, n) {
				full := filepath.Join(repoAbs, p)
				if st, err := os.Stat(full); err == nil && !st.IsDir() {
					return filepath.ToSlash(p)
				}
			}
		}
	}
	// No disk evidence: fall back to framework preference for which suffix to strip.
	fallback := sourceNameFromJSTSTestBaseFirst(fileName, testFramework)
	cands := sourcePathCandidates(mirrorPath, fallback)
	if len(cands) == 0 {
		return ""
	}
	return filepath.ToSlash(cands[0])
}

// sourceNameFromJSTSTestBaseFirst returns the framework-preferred source filename from the candidate
// set; when neither suffix matches it returns the input filename so callers still produce *some* path.
func sourceNameFromJSTSTestBaseFirst(fileName, testFramework string) string {
	if s, ok := sourceNameFromJSTSTestBase(fileName, testFramework); ok {
		return s
	}
	return fileName
}
