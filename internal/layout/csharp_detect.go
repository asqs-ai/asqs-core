package layout

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/teststack"
)

// E2ERootDirCandidates are repo-root directory names that indicate a dedicated end-to-end test tree
// (the E2E analogue of DedicatedRootDirCandidates). First existing match wins.
var E2ERootDirCandidates = []string{
	"e2e", "E2E", "e2e-tests", "e2e_tests", "EndToEnd", "endtoend", "integration", "IntegrationTests",
}

const maxCsprojWalkDepth = 8

// csprojWalkSkipDir delegates to dotnetproj.WalkSkipDir: there were five of these lists and they
// disagreed, so two walks over the same tree descended into different build output.
func csprojWalkSkipDir(name string) bool {
	return dotnetproj.WalkSkipDir(name)
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
	inSolution := solutionProjectDirs(repoAbs)
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
		score := testProjectDirScore(dir, wantE2E, inSolution)
		if !found || score > bestScore {
			best, bestScore, found = dir, score, true
		}
		return nil
	})
	return best, found
}

// testProjectDirScore ranks a candidate test project directory.
//
// Solution membership outranks everything else because it decides whether the tests run at all: the
// evaluator's compile and test steps both name the solution, so a project outside it is never built
// and never executed. Before this, every candidate under tests/ one level deep scored identically
// and the winner was whichever the directory walk reached first — alphabetical order, which is how
// run api-4198e8aa94b1aad506e17a3a6ff8f74b put ten unit tests into an Aspire integration-test
// project that no test run touched.
func testProjectDirScore(relDir string, wantE2E bool, inSolution map[string]bool) int {
	roots := DedicatedRootDirCandidates
	if wantE2E {
		roots = E2ERootDirCandidates
	}
	score := 10
	if len(inSolution) > 0 && inSolution[relDir] {
		score += 1000
	}
	if firstSegmentMatches(relDir, roots) {
		score += 100
	}
	if relDir != "" {
		score -= strings.Count(relDir, "/") // prefer shallower
	}
	return score
}

// solutionProjectDirs is the set of repo-relative directories holding a project a root solution
// lists. Empty when the repository has no root solution, which leaves the scoring unchanged.
func solutionProjectDirs(repoAbs string) map[string]bool {
	paths, err := dotnetproj.CsprojPathsFromRootSolutions(repoAbs)
	if err != nil || len(paths) == 0 {
		return nil
	}
	out := make(map[string]bool, len(paths))
	for _, abs := range paths {
		rel, rerr := filepath.Rel(repoAbs, filepath.Dir(abs))
		if rerr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		out[rel] = true
	}
	return out
}

// DetectCSharpUnitTestProjectDir returns the repo-relative directory of an existing C# unit-test
// project (excluding Playwright/E2E projects), or "" if none. Lets generated unit tests be routed into
// an existing test project regardless of where it lives in the tree.
//
// WHERE a generated test lands decides whether it ever runs, because the evaluator builds and tests
// the SOLUTION: a project the solution does not list is a project whose tests are never compiled and
// never executed, and the run still reports compile=ok because the solution it compiled never saw
// them. A validation run wrote all ten of its unit tests into an Aspire
// integration-test project and not one of them ran.
//
// Two sources answer better than the scoring, and both are consulted first:
//
//   - the bootstrap's own contract. It already chose a project, verified the stack on it, and added
//     the packages a generated test needs TO THAT PROJECT. Re-deriving the answer here is what let
//     the two disagree;
//   - the root solution's project list. The bootstrap picks from it (testbootstrap.primaryCsprojAbs)
//     and the evaluator builds it, so a candidate outside it is a candidate whose tests do not run.
func DetectCSharpUnitTestProjectDir(repoAbs string) string {
	if dir := bootstrapTestProjectDir(repoAbs); dir != "" {
		return dir
	}
	dir, _ := detectCSharpTestProjectDir(repoAbs, false)
	return dir
}

// bootstrapTestProjectDir returns the directory of the project named by the bootstrap → generation
// contract, when there is one and it is a C# project that still exists.
//
// Absence is normal and is the contract's own rule: bootstrap is off by default, a repository with a
// complete stack is skipped, and a checkout may predate the file. Every one of those returns "" and
// the detection below carries on exactly as it did.
func bootstrapTestProjectDir(repoAbs string) string {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs == "" {
		return ""
	}
	c, ok := teststack.Read(repoAbs)
	if !ok {
		return ""
	}
	rel := strings.TrimSpace(filepath.ToSlash(c.TestProject))
	if rel == "" || !strings.EqualFold(filepath.Ext(rel), ".csproj") {
		return ""
	}
	if st, err := os.Stat(filepath.Join(repoAbs, filepath.FromSlash(rel))); err != nil || st.IsDir() {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return ""
	}
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
