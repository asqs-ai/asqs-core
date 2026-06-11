package testbootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/layout"
)

// splitCSharpProdAndTestCsprojs classifies the repo's SDK-style projects into production projects and
// existing test projects (those already referencing a .NET test framework). MAUI/mobile/desktop
// workload projects are excluded from the production set so the generated test project does not
// reference projects that cannot build in a stock dotnet SDK container.
func splitCSharpProdAndTestCsprojs(repo string) (prod, test []string, err error) {
	paths, err := discoverSDKStyleCsprojPaths(repo)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		s := string(b)
		switch {
		case csprojHasDotNetTestFrameworkContent(s):
			test = append(test, p)
		case csprojRequiresOptionalDotnetWorkloadContent(s):
			// skip: MAUI/mobile/desktop projects don't build in stock SDK containers
		default:
			prod = append(prod, p)
		}
	}
	sort.Strings(prod)
	sort.Strings(test)
	return prod, test, nil
}

var reCsprojTargetFramework = regexp.MustCompile(`(?i)<TargetFrameworks?>\s*([^<]+)</TargetFrameworks?>`)

// netMajorFromTFM returns the major version of a netX.Y TFM (net8.0 -> 8, net8.0-windows -> 8), or 0
// for non-net / netstandard / netcoreapp monikers.
func netMajorFromTFM(tfm string) int {
	low := strings.ToLower(strings.TrimSpace(tfm))
	if !strings.HasPrefix(low, "net") {
		return 0
	}
	rest := strings.TrimPrefix(low, "net")
	if i := strings.IndexByte(rest, '.'); i > 0 {
		if n, err := strconv.Atoi(rest[:i]); err == nil {
			return n
		}
	}
	return 0
}

// inferCSharpTestTFM picks a target framework for the generated test project: the highest netX.0 among
// the production projects (a higher TFM can reference lower ones). Falls back to fallbackTFM, then net8.0.
func inferCSharpTestTFM(prodCsprojs []string, fallback string) string {
	maxMajor := 0
	for _, p := range prodCsprojs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		m := reCsprojTargetFramework.FindStringSubmatch(string(b))
		if m == nil {
			continue
		}
		for _, tfm := range strings.Split(m[1], ";") {
			if maj := netMajorFromTFM(tfm); maj > maxMajor {
				maxMajor = maj
			}
		}
	}
	if maxMajor > 0 {
		return "net" + strconv.Itoa(maxMajor) + ".0"
	}
	if f := strings.TrimSpace(fallback); f != "" {
		return f
	}
	return "net8.0"
}

// sanitizeDotnetProjectName keeps only characters valid in a .NET project name.
func sanitizeDotnetProjectName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" {
		out = "App"
	}
	return out
}

// dedicatedCSharpProjectBaseName derives "<Solution|Repo>" used to name the generated test projects.
func dedicatedCSharpProjectBaseName(repo string) string {
	if slns, _ := discoverRootSolutionFilePaths(repo); len(slns) > 0 {
		base := strings.TrimSuffix(filepath.Base(slns[0]), filepath.Ext(slns[0]))
		if n := sanitizeDotnetProjectName(base); n != "" {
			return n
		}
	}
	return sanitizeDotnetProjectName(filepath.Base(repo))
}

func projectReferencesRel(testDir string, prodCsprojs []string) []string {
	var out []string
	for _, p := range prodCsprojs {
		rel, err := filepath.Rel(testDir, p)
		if err != nil {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func renderCSharpTestProject(tfm string, useCPM bool, projRefs []string, withPlaywright bool) string {
	pkgLines := []string{csharpPkgTestSDK, csharpPkgXunit, csharpPkgRunner}
	if useCPM {
		pkgLines = []string{csharpPkgTestSDKCPM, csharpPkgXunitCPM, csharpPkgRunnerCPM}
	}
	if withPlaywright {
		if useCPM {
			pkgLines = append(pkgLines, csharpPkgPlaywrightCPM)
		} else {
			pkgLines = append(pkgLines, csharpPkgPlaywright)
		}
	}
	var refs strings.Builder
	for _, r := range projRefs {
		fmt.Fprintf(&refs, "    <ProjectReference Include=%q />\n", r)
	}
	return fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">

  <PropertyGroup>
    <TargetFramework>%s</TargetFramework>
    <ImplicitUsings>enable</ImplicitUsings>
    <Nullable>enable</Nullable>
    <IsPackable>false</IsPackable>
    <IsTestProject>true</IsTestProject>
  </PropertyGroup>

  <ItemGroup>
%s
  </ItemGroup>

  <ItemGroup>
%s  </ItemGroup>

</Project>
`, tfm, strings.Join(pkgLines, "\n"), refs.String())
}

// dedicatedProjectSpec parameterizes the unit vs E2E dedicated-test-project layout.
type dedicatedProjectSpec struct {
	rootDefault    string                      // "tests" or "e2e" (used when no matching root dir exists yet)
	nameSuffix     string                      // ".Tests" or ".E2E"
	detectRoot     func(repoAbs string) string // existing root dir for this kind (DetectDedicatedRoot / DetectE2ERoot)
	withPlaywright bool
}

// writeDedicatedCSharpTestProject creates a single test project under its root (existing dir, else the
// spec default) referencing every production project, so generated tests are routed there instead of
// scattered into production projects that cannot compile them. Honors Central Package Management.
// Idempotent: if the project already exists it returns its path with no changes.
func writeDedicatedCSharpTestProject(repo, gitRoot string, prodCsprojs []string, fallbackTFM string, spec dedicatedProjectSpec) (testProjAbs string, changed []string, err error) {
	if len(prodCsprojs) == 0 {
		return "", nil, fmt.Errorf("no production .csproj to reference")
	}
	root := spec.detectRoot(repo)
	if root == "" {
		root = spec.rootDefault
	}
	testDir := filepath.Join(repo, root)
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		return "", nil, err
	}
	testProjAbs = filepath.Join(testDir, dedicatedCSharpProjectBaseName(repo)+spec.nameSuffix+".csproj")
	if _, statErr := os.Stat(testProjAbs); statErr == nil {
		return testProjAbs, nil, nil // already present
	}

	tfm := inferCSharpTestTFM(prodCsprojs, fallbackTFM)

	// Central Package Management: when a Directory.Packages.props governs versions, the project must
	// not pin Version on PackageReference; merge PackageVersion entries into the props instead.
	ceiling := centralPackageManagementSearchCeiling(repo, gitRoot, testProjAbs)
	propsPath := findCentralPackageProps(ceiling, testDir)
	useCPM := propsPath != ""
	if useCPM {
		cpmPkgs := map[string]string{
			"Microsoft.NET.Test.Sdk":    VersionDotNetTestSDK,
			"xunit":                     VersionXunit,
			"xunit.runner.visualstudio": VersionXunitRunnerVS,
		}
		if spec.withPlaywright {
			cpmPkgs["Microsoft.Playwright"] = VersionMicrosoftPlaywrightNuGet
		}
		if propsChanged, e := ensureCentralPackageVersions(propsPath, cpmPkgs); e != nil {
			return "", nil, fmt.Errorf("Directory.Packages.props: %w", e)
		} else if propsChanged {
			changed = append(changed, propsPath)
		}
	}

	content := renderCSharpTestProject(tfm, useCPM, projectReferencesRel(testDir, prodCsprojs), spec.withPlaywright)
	if err := atomicWrite(testProjAbs, []byte(content)); err != nil {
		return "", nil, err
	}
	changed = append(changed, testProjAbs)
	return testProjAbs, dedupeAbsPaths(changed), nil
}

// createDedicatedCSharpTestProject creates a dedicated xUnit UNIT test project under the tests/ root.
func createDedicatedCSharpTestProject(repo, gitRoot string, prodCsprojs []string, fallbackTFM string) (testProjAbs string, changed []string, err error) {
	return writeDedicatedCSharpTestProject(repo, gitRoot, prodCsprojs, fallbackTFM, dedicatedProjectSpec{
		rootDefault: "tests",
		nameSuffix:  ".Tests",
		detectRoot:  layout.DetectDedicatedRoot,
	})
}

// createDedicatedCSharpE2EProject creates a dedicated Playwright + xUnit E2E test project under the
// e2e/ root, kept separate from the unit test project (different top-level root => no glob conflict).
func createDedicatedCSharpE2EProject(repo, gitRoot string, prodCsprojs []string, fallbackTFM string) (testProjAbs string, changed []string, err error) {
	return writeDedicatedCSharpTestProject(repo, gitRoot, prodCsprojs, fallbackTFM, dedicatedProjectSpec{
		rootDefault:    "e2e",
		nameSuffix:     ".E2E",
		detectRoot:     layout.DetectE2ERoot,
		withPlaywright: true,
	})
}

// firstCsprojInDir returns the first (lexicographically) .csproj directly in absDir, or "".
func firstCsprojInDir(absDir string) string {
	ents, err := os.ReadDir(absDir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".csproj") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(absDir, names[0])
}

// ensureCSharpE2EProjectForBootstrap returns the .csproj the Playwright bootstrap should target: an
// existing E2E project if present, else a freshly created dedicated e2e/ project (so E2E packages and
// tests do not pollute a production project), else the legacy primary project for single-project repos.
// changed is non-empty only when a new project was created.
func ensureCSharpE2EProjectForBootstrap(repo, gitRoot, fallbackTFM string) (csprojAbs string, changed []string, err error) {
	if dir := layout.DetectCSharpE2EProjectDir(repo); dir != "" {
		if cp := firstCsprojInDir(filepath.Join(repo, filepath.FromSlash(dir))); cp != "" {
			return cp, nil, nil
		}
	}
	if prod, _, derr := splitCSharpProdAndTestCsprojs(repo); derr == nil && len(prod) > 0 {
		return createDedicatedCSharpE2EProject(repo, gitRoot, prod, fallbackTFM)
	}
	cp, perr := primaryCsprojAbs(repo)
	return cp, nil, perr
}

func migrateWalkSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "bin", "obj", ".git", "node_modules", ".vs", "packages":
		return true
	}
	return false
}

// csFileIsMisplacedTest reports whether a .cs file is a test that was written into a production
// project (filename ends in Test/Tests AND it references a unit-test framework). Deliberately
// conservative to avoid relocating production helpers.
func csFileIsMisplacedTest(fileName, content string) bool {
	base := strings.ToLower(strings.TrimSuffix(fileName, ".cs"))
	if !strings.HasSuffix(base, "tests") && !strings.HasSuffix(base, "test") {
		return false
	}
	low := strings.ToLower(content)
	return strings.Contains(low, "using xunit") || strings.Contains(low, "[fact") || strings.Contains(low, "[theory") ||
		strings.Contains(low, "using nunit") || strings.Contains(low, "[test]") || strings.Contains(low, "[testmethod]") ||
		strings.Contains(low, "using microsoft.visualstudio.testtools")
}

// migrateStrayCSharpTestsIntoTestRoot moves test .cs files found inside production project trees into
// the unit test project's root (mirrored from the source layout), so production projects compile
// without referencing a test framework. Returns repo-relative descriptions of what moved.
func migrateStrayCSharpTestsIntoTestRoot(repo, testRootRel string, prodCsprojs []string) []string {
	testRootRel = filepath.ToSlash(strings.Trim(testRootRel, "/"))
	if testRootRel == "" {
		return nil
	}
	testRootAbs := filepath.Join(repo, filepath.FromSlash(testRootRel))
	var moved []string
	for _, prodCsproj := range prodCsprojs {
		prodDir := filepath.Dir(prodCsproj)
		_ = filepath.WalkDir(prodDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != prodDir && migrateWalkSkipDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(d.Name()), ".cs") {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil || !csFileIsMisplacedTest(d.Name(), string(b)) {
				return nil
			}
			relFromRepo, e := filepath.Rel(repo, path)
			if e != nil {
				return nil
			}
			relFromRepo = filepath.ToSlash(relFromRepo)
			if relFromRepo == "" || strings.HasPrefix(relFromRepo+"/", testRootRel+"/") {
				return nil // already under the test root
			}
			mirror := layout.MirrorDirForTests(filepath.ToSlash(filepath.Dir(relFromRepo)))
			target := filepath.Join(testRootAbs, filepath.FromSlash(mirror), d.Name())
			if _, statErr := os.Stat(target); statErr == nil {
				// A canonical copy already exists; just drop the stray so production compiles.
				if os.Remove(path) == nil {
					moved = append(moved, relFromRepo+" (removed duplicate)")
				}
				return nil
			}
			if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
				return nil
			}
			if rnErr := os.Rename(path, target); rnErr != nil {
				// cross-device fallback
				if data, re := os.ReadFile(path); re == nil && atomicWrite(target, data) == nil {
					_ = os.Remove(path)
				} else {
					return nil
				}
			}
			tRel, _ := filepath.Rel(repo, target)
			moved = append(moved, relFromRepo+" -> "+filepath.ToSlash(tRel))
			return nil
		})
	}
	return moved
}

// relocateStrayCSharpTests best-effort moves test files that ended up inside production projects (e.g.
// from a run before dedicated placement existed) into the unit test project, so production projects
// compile. No-op when no unit test project / production projects are found.
func relocateStrayCSharpTests(ctx context.Context, repo string, audit Auditor) {
	root := layout.DetectCSharpUnitTestProjectDir(repo)
	if root == "" {
		return
	}
	prod, _, err := splitCSharpProdAndTestCsprojs(repo)
	if err != nil || len(prod) == 0 {
		return
	}
	moved := migrateStrayCSharpTestsIntoTestRoot(repo, root, prod)
	if len(moved) == 0 {
		return
	}
	logAudit(audit, ctx, "test_bootstrap.relocated_stray_tests", map[string]interface{}{
		"message": fmt.Sprintf("Relocated %d stray test file(s) out of production projects into %s/", len(moved), root),
		"moved":   moved,
		"count":   len(moved),
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: relocated %d stray test file(s) out of production projects into %s/\n", len(moved), root)
}

// addCSharpTestProjectToSolutions best-effort adds the new test project to every root .sln/.slnx so the
// evaluator's `dotnet test <solution>` builds and runs it. Failures are logged, not fatal.
func addCSharpTestProjectToSolutions(ctx context.Context, ed *EphemeralDocker, repo, testProjAbs string, audit Auditor) {
	slns, err := discoverRootSolutionFilePaths(repo)
	if err != nil || len(slns) == 0 {
		return
	}
	projRel := relPathForBootstrap(repo, testProjAbs)
	for _, sln := range slns {
		slnRel := relPathForBootstrap(repo, sln)
		out, runErr := RunArgv(ctx, ed, repo, []string{"dotnet", "sln", slnRel, "add", projRel}, nil)
		if runErr != nil {
			logAuditError(audit, ctx, "test_bootstrap.sln_add_failed", map[string]interface{}{
				"message":  fmt.Sprintf("Could not add %s to %s (continuing): %v", projRel, slnRel, runErr),
				"solution": slnRel,
				"output":   truncate(string(out), 2000),
			})
			continue
		}
		logAudit(audit, ctx, "test_bootstrap.sln_add", map[string]interface{}{
			"message":  fmt.Sprintf("Added %s to %s", projRel, slnRel),
			"solution": slnRel,
		})
	}
}
