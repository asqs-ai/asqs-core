package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	csharpPkgTestSDK = `    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="` + VersionDotNetTestSDK + `" />`
	csharpPkgXunit   = `    <PackageReference Include="xunit" Version="` + VersionXunit + `" />`
	csharpPkgRunner  = `    <PackageReference Include="xunit.runner.visualstudio" Version="` + VersionXunitRunnerVS + `">
      <IncludeAssets>runtime; build; native; contentfiles; analyzers; buildtransitive</IncludeAssets>
      <PrivateAssets>all</PrivateAssets>
    </PackageReference>`
	csharpPkgTestSDKCPM = `    <PackageReference Include="Microsoft.NET.Test.Sdk" />`
	csharpPkgXunitCPM   = `    <PackageReference Include="xunit" />`
	csharpPkgRunnerCPM  = `    <PackageReference Include="xunit.runner.visualstudio">
      <IncludeAssets>runtime; build; native; contentfiles; analyzers; buildtransitive</IncludeAssets>
      <PrivateAssets>all</PrivateAssets>
    </PackageReference>`
	csharpPkgPlaywright    = `    <PackageReference Include="Microsoft.Playwright" Version="` + VersionMicrosoftPlaywrightNuGet + `" />`
	csharpPkgPlaywrightCPM = `    <PackageReference Include="Microsoft.Playwright" />`
)

// rootCsprojFiles returns sorted basenames of *.csproj at dir (non-recursive).
func rootCsprojFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".csproj") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// reCsprojSdkAttr captures the Sdk attribute value on the <Project> element.
var reCsprojSdkAttr = regexp.MustCompile(`(?i)<Project[^>]*\bSdk\s*=\s*["']([^"']+)["']`)

// isSdkStyleCsproj reports whether a project uses the modern SDK format.
//
// It matches the whole Microsoft.NET.Sdk FAMILY, not just the bare value. The previous exact match on
// `Sdk="Microsoft.NET.Sdk"` excluded `Microsoft.NET.Sdk.Web`, which made every ASP.NET Core project
// invisible to discovery: a Web API solution reported "no SDK-style .csproj found under repo", and in
// a Web+library solution the generated test project referenced only the library, so no controller was
// reachable from a test. Legacy non-SDK projects (ToolsVersion + Microsoft.CSharp.targets import)
// still do not match, which is the distinction that matters.
func isSdkStyleCsproj(content string) bool {
	m := reCsprojSdkAttr.FindStringSubmatch(content)
	if m == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[1])), "microsoft.net.sdk")
}

// reDotnetOptionalWorkloadTFM matches TFMs that need optional SDK workloads (MAUI / mobile / Windows desktop packs).
// Plain net8.0 / net10.0 do not match; net10.0-android etc. do (NETSDK1147 in stock dotnet/sdk Docker images).
var reDotnetOptionalWorkloadTFM = regexp.MustCompile(`(?i)net\d+\.\d+-(android|ios|iossimulator|maccatalyst|macos|tvos|watchos|windows)(\s|;|"|'|</|<!--|$)`)

// csprojRequiresOptionalDotnetWorkloadContent is true when build/test usually needs workloads not present in default SDK containers.
func csprojRequiresOptionalDotnetWorkloadContent(content string) bool {
	low := strings.ToLower(content)
	if strings.Contains(low, `sdk="microsoft.maui.sdk`) || strings.Contains(low, `sdk='microsoft.maui.sdk`) {
		return true
	}
	if strings.Contains(low, "<usemaui>true</usemaui>") {
		return true
	}
	return reDotnetOptionalWorkloadTFM.MatchString(content)
}

func csprojRequiresOptionalDotnetWorkload(csprojPath string) bool {
	b, err := os.ReadFile(csprojPath)
	if err != nil {
		return false
	}
	return csprojRequiresOptionalDotnetWorkloadContent(string(b))
}

// pickRootCsprojForBootstrap returns a root-level SDK-style .csproj, preferring projects that do not require
// MAUI/mobile/desktop workloads so `dotnet test` in ephemeral Docker succeeds without `dotnet workload install`.
// csprojHasDotNetTestFrameworkContent is true when the project already references a typical .NET test stack
// (Microsoft.NET.Test.Sdk, xUnit, NUnit, or MSTest). Used with .sln discovery so we do not patch a library/app
// project when a test project already exists in the solution.
func csprojHasDotNetTestFrameworkContent(content string) bool {
	s := strings.ToLower(content)
	if strings.Contains(s, "microsoft.net.test.sdk") {
		return true
	}
	if strings.Contains(s, `include="xunit"`) || strings.Contains(s, "xunit.core") {
		return true
	}
	if strings.Contains(s, `include="nunit`) || strings.Contains(s, "nunit.framework") {
		return true
	}
	if strings.Contains(s, "mstest.testframework") || strings.Contains(s, `include="mstest.test`) {
		return true
	}
	if strings.Contains(s, `include="mstest"`) {
		return true
	}
	return false
}

// csprojTestProjectLikenessScore is higher when the path/name looks like a unit-test project (e.g. *.Tests,
// under /tests/). Used to choose where to add xUnit when no solution project has test packages yet.
func csprojTestProjectLikenessScore(abs string) int {
	p := strings.ToLower(filepath.ToSlash(abs))
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(abs), ".csproj"))
	score := 0
	if strings.Contains(p, "/tests/") || strings.Contains(p, "/test/") {
		score += 100
	}
	if strings.Contains(base, "test") {
		score += 40
	}
	if strings.HasSuffix(base, ".tests") || strings.HasSuffix(base, "tests") {
		score += 30
	}
	return score
}

func pickRootCsprojForBootstrap(repo string) (string, error) {
	names, err := rootCsprojFiles(repo)
	if err != nil {
		return "", err
	}
	var workloadOnly []string
	for _, name := range names {
		abs := filepath.Join(repo, name)
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		s := string(b)
		if !isSdkStyleCsproj(s) {
			continue
		}
		if csprojRequiresOptionalDotnetWorkloadContent(s) {
			workloadOnly = append(workloadOnly, abs)
			continue
		}
		return abs, nil
	}
	if len(workloadOnly) > 0 {
		return workloadOnly[0], nil
	}
	return "", nil
}

// applyCSharpTestPackages adds the profile's missing test PackageReferences (SDK-style csproj only).
//
// This replaced applyCSharpXUnit, which added exactly Microsoft.NET.Test.Sdk + xunit + the VS runner
// regardless of what the solution was. That set cannot host what generation writes: the generation
// contract steers the model to Moq, generated C# assertions idiomatically use FluentAssertions, and
// an ASP.NET Core project needs Microsoft.AspNetCore.Mvc.Testing — none of which were ever added.
//
// When Directory.Packages.props exists above the project, uses Central Package Management (no Version
// on PackageReference) and merges PackageVersion entries into that props file.
// gitRepoRoot is the full clone root when repoRoot is a mono-repo subfolder; empty means use repoRoot.
// Returns absolute paths of files that were modified, plus the packages actually added.
func applyCSharpTestPackages(repoRoot, csprojPath, gitRepoRoot string, prof csharpTestProfile) (changedFiles []string, added []csharpPkg, err error) {
	b, err := os.ReadFile(csprojPath)
	if err != nil {
		return nil, nil, err
	}
	s := string(b)
	orig := s
	if !isSdkStyleCsproj(s) {
		return nil, nil, fmt.Errorf("only SDK-style .csproj is supported (expected Sdk=\"Microsoft.NET.Sdk\")")
	}

	missing := prof.missingPackages(s)
	if len(missing) == 0 {
		return nil, nil, nil
	}

	ceiling := centralPackageManagementSearchCeiling(repoRoot, gitRepoRoot, csprojPath)
	propsPath := findCentralPackageProps(ceiling, filepath.Dir(csprojPath))
	useCPM := propsPath != ""

	if useCPM {
		propsChanged, cerr := ensureCentralPackageVersions(propsPath, cpmVersionMap(missing))
		if cerr != nil {
			return nil, nil, fmt.Errorf("Directory.Packages.props: %w", cerr)
		}
		if propsChanged {
			changedFiles = append(changedFiles, propsPath)
		}
	}

	refs := make([]string, 0, len(missing))
	for _, pkg := range missing {
		refs = append(refs, renderCSharpPackageRef(pkg, useCPM))
	}
	block := "  <ItemGroup>\n" + strings.Join(refs, "\n") + "\n  </ItemGroup>\n"
	s = insertBeforeClosingCsproj(s, block)
	if s == orig {
		return nil, nil, fmt.Errorf(".csproj: could not find closing </Project>")
	}
	if err := atomicWrite(csprojPath, []byte(s)); err != nil {
		return nil, nil, err
	}
	changedFiles = append(changedFiles, csprojPath)
	return dedupeAbsPaths(changedFiles), missing, nil
}

func dedupeAbsPaths(paths []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range paths {
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func insertBeforeClosingCsproj(csproj, block string) string {
	lower := strings.ToLower(csproj)
	idx := strings.LastIndex(lower, "</project>")
	if idx < 0 {
		return csproj
	}
	return csproj[:idx] + block + csproj[idx:]
}

// applyCSharpPlaywrightPackage adds a Microsoft.Playwright PackageReference when missing.
// With Central Package Management (Directory.Packages.props), writes PackageVersion there and omits Version on the reference.
// gitRepoRoot is the full clone root when repoRoot is a mono subfolder; empty means use repoRoot for CPM search.
func applyCSharpPlaywrightPackage(repoRoot, csprojPath string, gitRepoRoot string) (changedFiles []string, err error) {
	b, err := os.ReadFile(csprojPath)
	if err != nil {
		return nil, err
	}
	s := string(b)
	orig := s
	if !isSdkStyleCsproj(s) {
		return nil, fmt.Errorf("only SDK-style .csproj is supported (expected Sdk=\"Microsoft.NET.Sdk\")")
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "microsoft.playwright") {
		return nil, nil
	}

	ceiling := centralPackageManagementSearchCeiling(repoRoot, gitRepoRoot, csprojPath)
	propsPath := findCentralPackageProps(ceiling, filepath.Dir(csprojPath))
	useCPM := propsPath != ""

	if useCPM {
		propsChanged, err := ensureCentralPackageVersions(propsPath, map[string]string{
			"Microsoft.Playwright": VersionMicrosoftPlaywrightNuGet,
		})
		if err != nil {
			return nil, fmt.Errorf("Directory.Packages.props: %w", err)
		}
		if propsChanged {
			changedFiles = append(changedFiles, propsPath)
		}
	}

	var line string
	if useCPM {
		line = csharpPkgPlaywrightCPM
	} else {
		line = csharpPkgPlaywright
	}
	block := "  <ItemGroup>\n" + line + "\n  </ItemGroup>\n"
	s = insertBeforeClosingCsproj(s, block)
	if s == orig {
		return nil, fmt.Errorf(".csproj: could not find closing </Project>")
	}
	if err := atomicWrite(csprojPath, []byte(s)); err != nil {
		return nil, err
	}
	changedFiles = append(changedFiles, csprojPath)
	return dedupeAbsPaths(changedFiles), nil
}
