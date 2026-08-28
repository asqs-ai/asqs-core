package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const aspNetCoreWebCsproj = `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <RootNamespace>Demo.App</RootNamespace>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Swashbuckle.AspNetCore" Version="6.6.2" />
  </ItemGroup>
</Project>`

const plainLibCsproj = `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
  </PropertyGroup>
</Project>`

func writeCsprojAt(t *testing.T, repo, relDir, name, body string) string {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(relDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, name)
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestDetectCSharpFramework_aspNetCore(t *testing.T) {
	repo := t.TempDir()
	writeCsprojAt(t, repo, "src/App", "App.csproj", aspNetCoreWebCsproj)
	writeCsprojAt(t, repo, "src/Core", "Core.csproj", plainLibCsproj)

	det, err := detectCSharpFramework(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if det.Framework != CSharpFrameworkAspNetCore {
		t.Fatalf("framework = %q (evidence %s)", det.Framework, det.Evidence)
	}
	if det.NetMajor != 8 || det.TargetFramework != "net8.0" {
		t.Errorf("TFM = %q major = %d", det.TargetFramework, det.NetMajor)
	}
	if filepath.Base(det.WebProjectAbs) != "App.csproj" {
		t.Errorf("web project = %s", det.WebProjectAbs)
	}
}

func TestDetectCSharpFramework_efCoreMajorFromPackageReference(t *testing.T) {
	repo := t.TempDir()
	writeCsprojAt(t, repo, "src/Data", "Data.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Microsoft.EntityFrameworkCore" Version="8.0.10" />
  </ItemGroup>
</Project>`)

	det, err := detectCSharpFramework(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !det.UsesEFCore || det.EFCoreMajor != 8 {
		t.Fatalf("UsesEFCore = %v major = %d", det.UsesEFCore, det.EFCoreMajor)
	}

	prof := buildCSharpTestProfile(det)
	got := strings.Join(describeCSharpPackages(prof.Packages), " ")
	if !strings.Contains(got, "Microsoft.EntityFrameworkCore.InMemory 8.0.") {
		t.Errorf("EF InMemory must match the EF major on the classpath; got %s", got)
	}
}

func TestBuildCSharpTestProfile_aspNetCorePinsMvcTestingToTheTFM(t *testing.T) {
	for _, tc := range []struct{ tfm, wantPrefix string }{
		{"net8.0", "8.0."},
		{"net9.0", "9.0."},
	} {
		det := csharpFrameworkDetection{
			Framework:       CSharpFrameworkAspNetCore,
			TestFramework:   CSharpTestXunit,
			TargetFramework: tc.tfm,
			NetMajor:        netMajorFromTFM(tc.tfm),
		}
		prof := buildCSharpTestProfile(det)
		var found string
		for _, p := range prof.Packages {
			if p.ID == "Microsoft.AspNetCore.Mvc.Testing" {
				found = p.Version
			}
		}
		// Mvc.Testing 10.x simply cannot be referenced from a net8.0 project: this must track the TFM.
		if !strings.HasPrefix(found, tc.wantPrefix) {
			t.Errorf("%s → Mvc.Testing %q, want %s*", tc.tfm, found, tc.wantPrefix)
		}
	}
}

func TestBuildCSharpTestProfile_alwaysIncludesMoqAndFluentAssertions(t *testing.T) {
	prof := buildCSharpTestProfile(csharpFrameworkDetection{
		Framework: CSharpFrameworkPlain, TestFramework: CSharpTestXunit, TargetFramework: "net8.0", NetMajor: 8,
	})
	got := strings.Join(describeCSharpPackages(prof.Packages), " ")
	for _, want := range []string{"Microsoft.NET.Test.Sdk", "xunit", "xunit.runner.visualstudio", "Moq", "FluentAssertions"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain profile missing %s; got %s", want, got)
		}
	}
	if prof.FrameworkSmoke != csharpSmokeNone {
		t.Error("a plain library has no host to boot")
	}
}

// TestFluentAssertionsPinStaysApache2 guards a licensing constraint, not a compatibility one:
// FluentAssertions 8.0.0 replaced the Apache-2.0 licence expression with a commercial licence file.
// ASQS writes this PackageReference into customer repositories.
func TestFluentAssertionsPinStaysApache2(t *testing.T) {
	if !strings.HasPrefix(VersionFluentAssertions, "7.") {
		t.Fatalf("VersionFluentAssertions = %q; the 8.x line requires a paid commercial licence", VersionFluentAssertions)
	}
}

func TestBuildCSharpTestProfile_workloadIsDeclined(t *testing.T) {
	prof := buildCSharpTestProfile(csharpFrameworkDetection{Framework: CSharpFrameworkWorkload})
	if !prof.Declined || prof.DeclinedReason == "" {
		t.Fatalf("MAUI/mobile/desktop solutions must be declined: %+v", prof)
	}
	if len(prof.Packages) != 0 {
		t.Error("a declined profile must not propose packages")
	}
}

func TestCSharpEstablishedTestFramework_honoursExistingChoice(t *testing.T) {
	repo := t.TempDir()
	nunit := writeCsprojAt(t, repo, "tests/Unit", "Unit.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="NUnit" Version="4.0.0" />
    <PackageReference Include="NUnit3TestAdapter" Version="4.5.0" />
  </ItemGroup>
</Project>`)
	if got := csharpEstablishedTestFramework([]string{nunit}); got != CSharpTestNUnit {
		t.Fatalf("got %q; bootstrap must complete the runner the team already uses, not add a second one", got)
	}
	if got := csharpEstablishedTestFramework(nil); got != CSharpTestXunit {
		t.Errorf("no established choice should default to xunit, got %q", got)
	}
}

func TestMissingPackages_exactIncludeMatch(t *testing.T) {
	prof := buildCSharpTestProfile(csharpFrameworkDetection{
		Framework: CSharpFrameworkPlain, TestFramework: CSharpTestXunit, TargetFramework: "net8.0", NetMajor: 8,
	})
	// Only the runner is present. "xunit" is a substring of "xunit.runner.visualstudio", so a naive
	// Contains check would wrongly conclude xunit itself is satisfied.
	csproj := `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
	  <PackageReference Include="xunit.runner.visualstudio" Version="2.8.2" />
	</ItemGroup></Project>`
	missing := prof.missingPackages(csproj)
	var ids []string
	for _, p := range missing {
		ids = append(ids, p.ID)
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "xunit,") && !strings.HasSuffix(joined, "xunit") {
		t.Errorf("xunit must still be reported missing; got %s", joined)
	}
	for _, id := range ids {
		if id == "xunit.runner.visualstudio" {
			t.Error("the present runner must not be reported missing")
		}
	}
}

// TestDetectCSharp_aspNetCoreWithBareXunitNeedsBootstrap is the C# analogue of the Spring Boot
// regression: a test project with only the xUnit runner passed the old detection and skipped
// bootstrap, leaving Moq, FluentAssertions and Mvc.Testing absent.
func TestDetectCSharp_aspNetCoreWithBareXunitNeedsBootstrap(t *testing.T) {
	repo := t.TempDir()
	writeCsprojAt(t, repo, "src/App", "App.csproj", aspNetCoreWebCsproj)
	writeCsprojAt(t, repo, "tests/App.Tests", "App.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.12.0" />
    <PackageReference Include="xunit" Version="2.9.2" />
    <PackageReference Include="xunit.runner.visualstudio" Version="2.8.2" />
  </ItemGroup>
</Project>`)

	rep, err := Detect(repo, "csharp")
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasFramework {
		t.Fatalf("an ASP.NET Core solution with only xUnit must still be bootstrapped: %s", rep.Reason)
	}
	for _, want := range []string{"Moq", "FluentAssertions", "Microsoft.AspNetCore.Mvc.Testing"} {
		if !strings.Contains(rep.Reason, want) {
			t.Errorf("reason should name missing %s; got: %s", want, rep.Reason)
		}
	}
}

func TestDetectCSharp_fullyEquippedIsSkipped(t *testing.T) {
	repo := t.TempDir()
	writeCsprojAt(t, repo, "src/App", "App.csproj", aspNetCoreWebCsproj)
	writeCsprojAt(t, repo, "tests/App.Tests", "App.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.12.0" />
    <PackageReference Include="xunit" Version="2.9.2" />
    <PackageReference Include="xunit.runner.visualstudio" Version="2.8.2" />
    <PackageReference Include="Moq" Version="4.20.72" />
    <PackageReference Include="FluentAssertions" Version="7.2.2" />
    <PackageReference Include="Microsoft.AspNetCore.Mvc.Testing" Version="8.0.30" />
  </ItemGroup>
</Project>`)

	rep, err := Detect(repo, "csharp")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasFramework {
		t.Fatalf("a complete stack needs nothing: %s", rep.Reason)
	}
}

func TestRenderCSharpPackageRef_cpmOmitsVersion(t *testing.T) {
	p := csharpPkg{ID: "Moq", Version: VersionMoq}
	if got := renderCSharpPackageRef(p, false); !strings.Contains(got, `Version="`+VersionMoq+`"`) {
		t.Errorf("non-CPM must pin: %s", got)
	}
	if got := renderCSharpPackageRef(p, true); strings.Contains(got, "Version=") {
		t.Errorf("CPM must not put Version on PackageReference: %s", got)
	}
	runner := csharpPkg{ID: "xunit.runner.visualstudio", Version: VersionXunitRunnerVS, PrivateRunner: true}
	if got := renderCSharpPackageRef(runner, false); !strings.Contains(got, "<PrivateAssets>all</PrivateAssets>") {
		t.Errorf("test adapters need PrivateAssets: %s", got)
	}
}

// TestIsSdkStyleCsproj_acceptsTheWholeSdkFamily guards the discovery bug that made every ASP.NET Core
// project invisible: an exact match on Sdk="Microsoft.NET.Sdk" excludes Microsoft.NET.Sdk.Web, so a
// Web API solution reported "no SDK-style .csproj found under repo".
func TestIsSdkStyleCsproj_acceptsTheWholeSdkFamily(t *testing.T) {
	for _, sdk := range []string{
		"Microsoft.NET.Sdk",
		"Microsoft.NET.Sdk.Web",
		"Microsoft.NET.Sdk.BlazorWebAssembly",
		"Microsoft.NET.Sdk.Razor",
		"Microsoft.NET.Sdk.Worker",
	} {
		if !isSdkStyleCsproj(`<Project Sdk="` + sdk + `">`) {
			t.Errorf("%s should be recognised as SDK-style", sdk)
		}
	}
	if !isSdkStyleCsproj(`<Project Sdk='Microsoft.NET.Sdk.Web'>`) {
		t.Error("single-quoted Sdk attribute should be recognised")
	}
	// Legacy, non-SDK projects must still be rejected — that is the distinction that matters.
	legacy := `<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003">
	  <Import Project="$(MSBuildToolsPath)\Microsoft.CSharp.targets" />
	</Project>`
	if isSdkStyleCsproj(legacy) {
		t.Error("legacy non-SDK project must not be treated as SDK-style")
	}
	if isSdkStyleCsproj(`<Project Sdk="Microsoft.Maui.Sdk">`) {
		t.Error("non Microsoft.NET.Sdk family SDKs must not match")
	}
}

// TestExcludeTestRootFromProductionProjects covers the collision a root-level production project
// creates: SDK projects glob **/*.cs downwards, so tests/ lands inside the production compile set and
// every generated test is compiled a second time without xUnit, Moq or FluentAssertions on the
// classpath (CS0246, attributed to the production project, unreachable by the fix loop).
func TestExcludeTestRootFromProductionProjects(t *testing.T) {
	repo := t.TempDir()
	prod := writeCsprojAt(t, repo, ".", "Minimal.csproj", plainLibCsproj)
	testDir := filepath.Join(repo, "tests")

	changed, err := excludeTestRootFromProductionProjects(testDir, []string{prod})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected the root project to be patched, got %v", changed)
	}
	b, _ := os.ReadFile(prod)
	if !strings.Contains(string(b), `<Compile Remove="tests/**" />`) {
		t.Fatalf("exclusion not written:\n%s", b)
	}

	again, err := excludeTestRootFromProductionProjects(testDir, []string{prod})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Error("second apply should be idempotent")
	}
}

func TestExcludeTestRootFromProductionProjects_leavesSiblingProjectsAlone(t *testing.T) {
	repo := t.TempDir()
	// src/App does not contain repo/tests, so its glob never sees the test sources.
	prod := writeCsprojAt(t, repo, "src/App", "App.csproj", plainLibCsproj)
	changed, err := excludeTestRootFromProductionProjects(filepath.Join(repo, "tests"), []string{prod})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("a project that does not glob tests/ must not be touched, got %v", changed)
	}
}
