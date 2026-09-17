package layout

import (
	"io/fs"
	"os"
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
	if projDir := csharpTestProjectDirForSource(repoAbs, sourceFileRel); projDir != "" {
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
	sourceName, ok := sourceNameFromCSharpTestBase(segs[len(segs)-1])
	if !ok {
		return ""
	}

	// The path INSIDE the test project is what mirrors the production layout. Finding the project
	// is what makes every .NET layout work: a test project sits wherever the solution puts it —
	// MyApp.Tests/ at the root, src/MyApp.Tests/, tests/MyApp.UnitTests/ — and requiring the first
	// segment to be a dedicated tests root answered for exactly one of them, so the other layouts
	// produced no mapping at all and the run could not tell which tests already cover a symbol.
	projectDir, inner := splitCSharpTestProjectPath(segs)
	var cands []string
	if projectDir != "" {
		mirror := filepath.FromSlash(strings.Join(inner, "/"))
		cands = append(cands, csharpSiblingProjectCandidates(projectDir, mirror, sourceName)...)
	}
	// The dedicated-root convention, which is what this understood before.
	if isDedicatedRootSegment(segs[0]) && len(segs) > 2 {
		cands = append(cands, sourcePathCandidates(filepath.FromSlash(strings.Join(segs[1:len(segs)-1], "/")), sourceName)...)
	}
	// The test's own directory: a repository that keeps FooTests.cs beside Foo.cs is the simplest
	// layout there is, and it is the only candidate available when the path names no test project
	// and sits under no dedicated root.
	if dir := strings.Join(segs[:len(segs)-1], "/"); dir != "" {
		cands = append(cands, filepath.ToSlash(filepath.Join(dir, sourceName)))
	}
	cands = append(cands, sourcePathCandidates("", sourceName)...)
	if got := firstExistingSourcePath(dedupeStrings(cands), repoAbs); got != "" {
		return got
	}
	// The mirror can miss entirely — a test organised by feature rather than by namespace. A
	// repository-wide search for the source NAME is the last resort, and only an unambiguous hit
	// counts: two files called PricingService.cs mean this cannot say which one is under test.
	return uniqueSourceFileNamed(repoAbs, sourceName, testFileRel)
}

// splitCSharpTestProjectPath finds the test-project segment in a path and returns the directory
// holding it plus the path inside it. `tests/MyApp.UnitTests/Services/FooTests.cs` yields
// ("tests", ["Services"]).
func splitCSharpTestProjectPath(segs []string) (projectDir string, inner []string) {
	for i := 0; i < len(segs)-1; i++ {
		if !csharpTestProjectSegment(segs[i]) {
			continue
		}
		return strings.Join(segs[:i], "/"), segs[i+1 : len(segs)-1]
	}
	return "", nil
}

// csharpTestProjectSegment recognises the directory name of a test PROJECT, which in .NET is also
// its assembly name: MyApp.Tests, MyApp.UnitTests, MyApp.IntegrationTests, MyApp.Specs.
func csharpTestProjectSegment(seg string) bool {
	low := strings.ToLower(seg)
	for _, suffix := range []string{".tests", ".test", ".unittests", ".integrationtests", ".specs"} {
		if strings.HasSuffix(low, suffix) {
			return true
		}
	}
	return false
}

// csharpSiblingProjectCandidates maps a test project's inner path onto its production siblings.
//
// `tests/MyApp.UnitTests/Services/Foo.cs` looks for `src/MyApp/Services/Foo.cs`: the production
// project is the test project's name with the test suffix removed, and it sits under one of the
// conventional roots rather than beside the test.
func csharpSiblingProjectCandidates(projectDir, mirror, sourceName string) []string {
	var out []string
	add := func(parts ...string) {
		out = append(out, filepath.ToSlash(filepath.Join(parts...)))
	}
	// Beside the test project, which is the layout when both sit under the same root.
	add(projectDir, mirror, sourceName)
	for _, root := range []string{"src", "source", ""} {
		if root == "" {
			add(mirror, sourceName)
			continue
		}
		add(root, mirror, sourceName)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// uniqueSourceFileNamed returns the one production file with this name, or "" when there is none or
// more than one. Ambiguity is silence: naming the wrong source would attribute a test's coverage to
// a symbol it never exercises.
func uniqueSourceFileNamed(repoAbs, sourceName, excludeRel string) string {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	if repoAbs == "" || repoAbs == "." || sourceName == "" {
		return ""
	}
	var found string
	count := 0
	_ = filepath.WalkDir(repoAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != repoAbs && dotnetproj.WalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != sourceName {
			return nil
		}
		rel, rerr := filepath.Rel(repoAbs, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == excludeRel || IsCSharpTestPath(rel) {
			return nil // a test is not the source it covers
		}
		count++
		found = rel
		return nil
	})
	if count != 1 {
		return ""
	}
	return found
}

// DetectCSharpWebProjectRel returns the repo-relative .csproj of the ASP.NET application a browser
// E2E test drives, or "" when there is none or the choice is ambiguous.
//
// "Ambiguous" is a real answer here, not a failure to try: a solution with two web projects — an
// API and a separate front end, say — gives no basis for picking one, and starting the wrong one
// produces a browser that navigates to an application the test was not written against. Saying
// nothing leaves the E2E step to fail on an unset base URL, which at least names what is missing.
//
// Test projects are excluded even when they carry the Web SDK, which an integration-test project
// sometimes does.
func DetectCSharpWebProjectRel(repoAbs string) string {
	root := filepath.Clean(strings.TrimSpace(repoAbs))
	if root == "" {
		return ""
	}
	var found []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && dotnetproj.WalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if IsCSharpTestPath(strings.TrimSuffix(rel, filepath.Ext(rel)) + ".cs") {
			return nil
		}
		facts, ferr := dotnetproj.ResolveFacts(root, path)
		if ferr != nil || !facts.IsWebSDK {
			return nil
		}
		found = append(found, rel)
		return nil
	})
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// csharpTestProjectDirForSource picks the test project a test for THIS source file belongs in.
//
// One test project for the whole repository is right for a single-solution repo and wrong for a
// mono-repo, which holds several independent .NET trees. A test can only name types its own project
// references, so a test for tree B placed in tree A cannot compile — and it fails in the worst way,
// because the namespace it needs exists in the repository, just not from there.
//
// A validation run is the case. Seven unit tests for one tree's sources were written
// into another tree's test project. The fixer spent eight rounds on them: the model
// alternated between the real NimblePros namespace, which that project cannot reference, and an
// invented Clean.Architecture one, which does not exist — and ASQS refused the second as an
// unresolved dependency every round, correctly and uselessly.
//
// The answer already exists elsewhere in the system: TestProjectReachableDirs computes what each
// test project can reference, and the planner uses it to decide which gaps are worth planning. This
// asks the same question in the other direction — not "may this gap be planned" but "from which
// project can it be written". A closure it cannot read in full comes back nil, which keeps the
// candidate out and leaves the repository-wide answer in place.
func csharpTestProjectDirForSource(repoAbs, sourceFileRel string) string {
	repoAbs = filepath.Clean(strings.TrimSpace(repoAbs))
	sourceFileRel = filepath.ToSlash(strings.TrimSpace(sourceFileRel))
	if repoAbs == "" || sourceFileRel == "" {
		return DetectCSharpUnitTestProjectDir(repoAbs)
	}

	inSolution := solutionProjectDirs(repoAbs)
	contractDir := bootstrapTestProjectDir(repoAbs)
	best, bestScore := "", -1
	for _, projRel := range dotnetproj.FindTestProjects(repoAbs) {
		dirs := dotnetproj.TestProjectReachableDirs(repoAbs, projRel)
		if len(dirs) == 0 || !dotnetproj.PathIsReachableFrom(sourceFileRel, dirs) {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(projRel))
		if dir == "." {
			dir = ""
		}
		if csharpTestProjectIsE2E(repoAbs, projRel, dir) {
			continue // unit tests do not belong in a Playwright project
		}
		score := testProjectDirScore(dir, false, inSolution)
		if dir == contractDir {
			// The bootstrap verified this one and installed the packages a generated test needs
			// into it, so among projects that can all reach the source it is the settled answer.
			score += 10000
		}
		if score > bestScore {
			best, bestScore = dir, score
		}
	}
	if best != "" {
		return best
	}
	// Nothing could be proven to reach this file: keep the repository-wide answer rather than
	// inventing a location from a closure that could not be read.
	return DetectCSharpUnitTestProjectDir(repoAbs)
}

// csharpTestProjectIsE2E mirrors detectCSharpTestProjectDir's own exclusion, so the source-scoped
// pick and the repository-wide one agree about what a unit-test project is.
func csharpTestProjectIsE2E(repoAbs, projRel, dirRel string) bool {
	if firstSegmentMatches(dirRel, E2ERootDirCandidates) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(repoAbs, filepath.FromSlash(projRel)))
	if err != nil {
		return false
	}
	return csprojReferencesPlaywrightContent(string(b))
}
