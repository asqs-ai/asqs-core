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

func isSdkStyleCsproj(content string) bool {
	s := strings.ToLower(content)
	return strings.Contains(s, `sdk="microsoft.net.sdk"`) || strings.Contains(s, `sdk='microsoft.net.sdk'`)
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

// applyCSharpXUnit adds Microsoft.NET.Test.Sdk + xUnit PackageReferences when missing (SDK-style csproj only).
// When Directory.Packages.props exists above the project, uses Central Package Management (no Version on PackageReference)
// and merges PackageVersion entries into that props file.
// gitRepoRoot is the full clone root when repoRoot is a mono-repo subfolder (e.g. mono_repo_test_workspace); empty means use repoRoot for CPM search.
// Returns absolute paths of files that were modified (csproj and/or Directory.Packages.props).
func applyCSharpXUnit(repoRoot, csprojPath string, gitRepoRoot string) (changedFiles []string, err error) {
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
	needSDK := !strings.Contains(lower, "microsoft.net.test.sdk")
	needXunit := !strings.Contains(lower, `include="xunit"`)
	needRunner := !strings.Contains(lower, "xunit.runner.visualstudio")

	if !needSDK && !needXunit && !needRunner {
		return nil, nil
	}

	ceiling := centralPackageManagementSearchCeiling(repoRoot, gitRepoRoot, csprojPath)
	propsPath := findCentralPackageProps(ceiling, filepath.Dir(csprojPath))
	useCPM := propsPath != ""

	if useCPM {
		pkgs := map[string]string{}
		if needSDK {
			pkgs["Microsoft.NET.Test.Sdk"] = VersionDotNetTestSDK
		}
		if needXunit {
			pkgs["xunit"] = VersionXunit
		}
		if needRunner {
			pkgs["xunit.runner.visualstudio"] = VersionXunitRunnerVS
		}
		propsChanged, err := ensureCentralPackageVersions(propsPath, pkgs)
		if err != nil {
			return nil, fmt.Errorf("Directory.Packages.props: %w", err)
		}
		if propsChanged {
			changedFiles = append(changedFiles, propsPath)
		}
	}

	var refs []string
	if needSDK {
		if useCPM {
			refs = append(refs, csharpPkgTestSDKCPM)
		} else {
			refs = append(refs, csharpPkgTestSDK)
		}
	}
	if needXunit {
		if useCPM {
			refs = append(refs, csharpPkgXunitCPM)
		} else {
			refs = append(refs, csharpPkgXunit)
		}
	}
	if needRunner {
		if useCPM {
			refs = append(refs, csharpPkgRunnerCPM)
		} else {
			refs = append(refs, csharpPkgRunner)
		}
	}
	block := "  <ItemGroup>\n" + strings.Join(refs, "\n") + "\n  </ItemGroup>\n"
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
