package layout

import (
	"path/filepath"
	"strings"
)

// csharpSourceDirLooksLikeTestTree is true when the path is already under a conventional tests folder
// (avoid tests/tests/… when the indexer points at a file inside the test tree).
func csharpSourceDirLooksLikeTestTree(dirRel string) bool {
	dirRel = filepath.ToSlash(strings.TrimSpace(dirRel))
	if dirRel == "" || dirRel == "." {
		return false
	}
	segs := strings.Split(dirRel, "/")
	first := segs[0]
	for _, c := range DedicatedRootDirCandidates {
		if strings.EqualFold(first, c) {
			return true
		}
	}
	low := strings.ToLower(dirRel)
	if strings.Contains(low, ".tests/") || strings.HasSuffix(low, ".tests") {
		return true
	}
	return false
}

// SuggestedCSharpUnitTestPath returns the repo-relative path for a new xUnit-style *Tests.cs file.
// When repoAbs is set and a dedicated tests directory exists at the repo root, tests are placed under it
// with directory layout mirrored from the source file (after stripping src/ or source/).
// Otherwise behavior matches the legacy sibling path (same directory as the source file).
func SuggestedCSharpUnitTestPath(sourceFileRel, repoAbs string) string {
	sourceFileRel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(sourceFileRel), "/"))
	if sourceFileRel == "" {
		return ""
	}
	base := filepath.Base(sourceFileRel)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	dir := filepath.Dir(sourceFileRel)
	testName := name + "Tests" + ext

	if csharpSourceDirLooksLikeTestTree(dir) {
		return filepath.Join(dir, testName)
	}

	// Prefer routing into an existing unit-test project's directory (wherever it lives in the tree),
	// so generated tests compile in that project instead of being scattered into production projects.
	if projDir := DetectCSharpUnitTestProjectDir(repoAbs); projDir != "" {
		mir := MirrorDirForTests(dir)
		if mir == "" {
			return filepath.Join(projDir, testName)
		}
		return filepath.Join(projDir, filepath.FromSlash(mir), testName)
	}

	// Never place a C# unit test beside its source: an SDK-style production .csproj would compile it
	// without referencing xUnit (CS0246). When no dedicated tests root exists yet, default to a tests/
	// tree (the same root the bootstrap creates the dedicated test project under).
	root := DetectDedicatedRoot(repoAbs)
	if root == "" {
		root = "tests"
	}
	mir := MirrorDirForTests(dir)
	if mir == "" {
		return filepath.Join(root, testName)
	}
	return filepath.Join(root, filepath.FromSlash(mir), testName)
}

func sourceNameFromCSharpTestBase(fileName string) (sourceName string, ok bool) {
	ext := filepath.Ext(fileName)
	name := strings.TrimSuffix(fileName, ext)
	if strings.HasSuffix(name, "Tests") && len(name) > 5 {
		return name[:len(name)-5] + ext, true
	}
	if strings.HasSuffix(name, "Test") && len(name) > 4 && !strings.EqualFold(name, "Test") {
		return name[:len(name)-4] + ext, true
	}
	return "", false
}

// SourcePathFromCSharpTestFile maps a test path under a dedicated root back to a likely production file path.
// When repoAbs is set, existing files are preferred (src/, source/, mirror). When repoAbs is empty or none exist,
// returns the src/… heuristic. Returns "" if the path is not under a dedicated root segment or basename is not a recognized C# test name.
func SourcePathFromCSharpTestFile(testFileRel, repoAbs string) string {
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
	sourceName, ok := sourceNameFromCSharpTestBase(fileName)
	if !ok {
		return ""
	}
	var mirrorPath string
	if len(segs) > 2 {
		mirrorPath = filepath.FromSlash(strings.Join(segs[1:len(segs)-1], "/"))
	}
	cands := sourcePathCandidates(mirrorPath, sourceName)
	return firstExistingSourcePath(cands, repoAbs)
}
