package layout

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// E2ERootDirCandidates are repo-root directory names that indicate a dedicated end-to-end test tree
// (the E2E analogue of DedicatedRootDirCandidates). First existing match wins.
var E2ERootDirCandidates = []string{
	"e2e", "E2E", "e2e-tests", "e2e_tests", "EndToEnd", "endtoend", "integration", "IntegrationTests",
}

const maxCsprojWalkDepth = 8

func csprojWalkSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "bin", "obj", "node_modules", "packages", ".vs", ".vscode", "testresults",
		"dist", "build", "out", "target", "__pycache__", ".next", ".idea":
		return true
	}
	return false
}

func relDirDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func csprojReferencesTestFrameworkContent(content string) bool {
	s := strings.ToLower(content)
	return strings.Contains(s, "microsoft.net.test.sdk") ||
		strings.Contains(s, `include="xunit"`) || strings.Contains(s, "xunit.core") ||
		strings.Contains(s, `include="nunit`) || strings.Contains(s, "nunit.framework") ||
		strings.Contains(s, "mstest.testframework") || strings.Contains(s, `include="mstest`)
}

func csprojReferencesPlaywrightContent(content string) bool {
	return strings.Contains(strings.ToLower(content), "microsoft.playwright")
}

// firstSegmentMatches reports whether the first path segment of relDir matches any candidate.
func firstSegmentMatches(relDir string, candidates []string) bool {
	relDir = filepath.ToSlash(strings.TrimSpace(relDir))
	if relDir == "" || relDir == "." {
		return false
	}
	seg := strings.SplitN(relDir, "/", 2)[0]
	for _, c := range candidates {
		if strings.EqualFold(seg, c) {
			return true
		}
	}
	return false
}

// detectCSharpTestProjectDir walks repoAbs for a .csproj that references a test framework and returns
// the best matching project's repo-relative directory. When wantE2E is true it selects Playwright /
// e2e-rooted test projects; otherwise it selects plain unit-test projects (excluding E2E ones). The
// best match prefers projects under a recognized tests/e2e root and shallower paths.
func detectCSharpTestProjectDir(repoAbs string, wantE2E bool) (relDir string, found bool) {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs == "" {
		return "", false
	}
	best, bestScore := "", -1
	_ = filepath.WalkDir(repoAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != repoAbs && csprojWalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if relDirDepth(repoAbs, path) > maxCsprojWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil || !csprojReferencesTestFrameworkContent(string(b)) {
			return nil
		}
		dir, e := filepath.Rel(repoAbs, filepath.Dir(path))
		if e != nil {
			return nil
		}
		dir = filepath.ToSlash(dir)
		if dir == "." {
			dir = ""
		}
		isE2E := csprojReferencesPlaywrightContent(string(b)) || firstSegmentMatches(dir, E2ERootDirCandidates)
		if isE2E != wantE2E {
			return nil
		}
		score := testProjectDirScore(dir, wantE2E)
		if !found || score > bestScore {
			best, bestScore, found = dir, score, true
		}
		return nil
	})
	return best, found
}

func testProjectDirScore(relDir string, wantE2E bool) int {
	roots := DedicatedRootDirCandidates
	if wantE2E {
		roots = E2ERootDirCandidates
	}
	score := 10
	if firstSegmentMatches(relDir, roots) {
		score += 100
	}
	if relDir != "" {
		score -= strings.Count(relDir, "/") // prefer shallower
	}
	return score
}

// DetectCSharpUnitTestProjectDir returns the repo-relative directory of an existing C# unit-test
// project (excluding Playwright/E2E projects), or "" if none. Lets generated unit tests be routed into
// an existing test project regardless of where it lives in the tree.
func DetectCSharpUnitTestProjectDir(repoAbs string) string {
	dir, _ := detectCSharpTestProjectDir(repoAbs, false)
	return dir
}

// DetectCSharpE2EProjectDir returns the repo-relative directory of an existing C# E2E (Playwright)
// test project, or "" if none.
func DetectCSharpE2EProjectDir(repoAbs string) string {
	dir, _ := detectCSharpTestProjectDir(repoAbs, true)
	return dir
}

// SuggestedCSharpE2ETestPath returns the repo-relative path for a new C# end-to-end *E2ETests.cs file:
// into an existing E2E project's directory (mirrored from the source), else under a dedicated e2e/ root.
func SuggestedCSharpE2ETestPath(sourceFileRel, repoAbs string) string {
	sourceFileRel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(sourceFileRel), "/"))
	if sourceFileRel == "" {
		return ""
	}
	base := filepath.Base(sourceFileRel)
	ext := filepath.Ext(base)
	if ext == "" {
		ext = ".cs"
	}
	name := strings.TrimSuffix(base, ext)
	dir := filepath.Dir(sourceFileRel)
	testName := name + "E2ETests" + ext

	root := DetectCSharpE2EProjectDir(repoAbs)
	if root == "" {
		if existing := DetectE2ERoot(repoAbs); existing != "" {
			root = existing
		} else {
			root = "e2e"
		}
	}
	mir := MirrorDirForTests(dir)
	if mir == "" {
		return filepath.Join(root, testName)
	}
	return filepath.Join(root, filepath.FromSlash(mir), testName)
}

// DetectE2ERoot returns the actual directory name at repoAbs if one of E2ERootDirCandidates exists, else "".
func DetectE2ERoot(repoAbs string) string {
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
	for _, cand := range E2ERootDirCandidates {
		if seen[cand] {
			return cand
		}
	}
	return ""
}
