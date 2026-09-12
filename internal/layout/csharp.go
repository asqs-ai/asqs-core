package layout

import (
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
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
		mir := csharpMirrorDirForTests(sourceFileRel, dir, repoAbs)
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
	mir := csharpMirrorDirForTests(sourceFileRel, dir, repoAbs)
	if mir == "" {
		return filepath.Join(root, testName)
	}
	return filepath.Join(root, filepath.FromSlash(mir), testName)
}

// csharpMirrorDirForTests returns the sub-path a test should sit at INSIDE the test project.
//
// The shared mirror strips `src/` and keeps everything after it, which leaves the source project's
// own directory name in the path: src/Shop/Pages/Orders became <test project>/Shop/Pages/Orders.
//
// In C# a folder is a namespace. A test project whose root namespace is Shop.Tests therefore reads
// that directory as Shop.Tests.Shop, and the segment `Shop` SHADOWS the top-level Shop namespace for
// every file in the project — so `using Shop.Core.Services;` resolves against Shop.Tests.Shop.Core
// and fails. A validation run is the case: one generated Razor test landed and the repository's
// own pre-existing PricingServiceTests.cs stopped compiling. Nothing could repair it
// from inside that file, because the shadow is created by a different file's namespace.
//
// Mirroring below the SOURCE PROJECT is both the fix and what the convention meant all along: a
// test for src/Shop/Pages/Orders/Index.cshtml.cs belongs at <test project>/Pages/Orders. When no
// project can be found the shared behaviour stands — there is no project directory to mirror
// against, and inventing one would be worse than a flat layout.
func csharpMirrorDirForTests(sourceFileRel, dir, repoAbs string) string {
	if repoAbs == "" {
		return MirrorDirForTests(dir)
	}
	csprojRel, ok := dotnetproj.NearestCsprojRel(repoAbs, sourceFileRel)
	if !ok {
		return MirrorDirForTests(dir)
	}
	projDir := filepath.ToSlash(filepath.Dir(csprojRel))
	if projDir == "." || projDir == "" {
		return MirrorDirForTests(dir)
	}
	d := filepath.ToSlash(dir)
	if d == projDir {
		return "" // the file sits at the project root: no subdirectory to mirror
	}
	if rest := strings.TrimPrefix(d, projDir+"/"); rest != d {
		return strings.Trim(rest, "/")
	}
	return MirrorDirForTests(dir)
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
