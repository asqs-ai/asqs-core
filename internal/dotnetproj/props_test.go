package dotnetproj

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const webCsprojNoTFM = `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Serilog.AspNetCore" Version="8.0.1" />
  </ItemGroup>
</Project>`

// The fixture's own shape: the TFM lives in Directory.Build.props, not in the .csproj. Every TFM
// consumer read the .csproj alone, so the test project was created at the net8.0 fallback and the
// eval image was picked from a repo that "declares no TFM".
func TestResolveFacts_tfmFromDirectoryBuildProps(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project>
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <LangVersion>12.0</LangVersion>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
  </PropertyGroup>
</Project>`)
	csproj := write(t, root, "src/App/App.csproj", webCsprojNoTFM)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want [net8.0]", f.TFMs)
	}
	if f.LangVersion != "12.0" {
		t.Errorf("LangVersion = %q, want 12.0", f.LangVersion)
	}
	if f.Nullable != "enable" {
		t.Errorf("Nullable = %q, want enable", f.Nullable)
	}
	if !f.ImplicitUsings {
		t.Error("ImplicitUsings = false, want true")
	}
	if len(f.PropsPaths) != 1 || filepath.Base(f.PropsPaths[0]) != "Directory.Build.props" {
		t.Errorf("PropsPaths = %v, want the one props file", f.PropsPaths)
	}
	if !f.IsWebSDK {
		t.Error("IsWebSDK = false for Microsoft.NET.Sdk.Web")
	}
}

// MSBuild evaluates the .csproj after the props file, so the nearer file wins.
func TestResolveFacts_csprojOverridesProps(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><PropertyGroup>
    <TargetFramework>net8.0</TargetFramework><LangVersion>11.0</LangVersion>
    <ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net9.0</TargetFramework>
    <ImplicitUsings>disable</ImplicitUsings>
  </PropertyGroup>
</Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net9.0"}) {
		t.Errorf("TFMs = %v, want [net9.0] (the project wins)", f.TFMs)
	}
	if f.LangVersion != "11.0" {
		t.Errorf("LangVersion = %q, want 11.0 inherited from props", f.LangVersion)
	}
	if f.ImplicitUsings {
		t.Error("ImplicitUsings = true, want the project's disable to win")
	}
	if f.IsWebSDK {
		t.Error("IsWebSDK = true for a plain Microsoft.NET.Sdk project")
	}
}

// The nearest props file shadows one higher up entirely, because MSBuild stops at the first file it
// finds walking up. See TestResolveFacts_walkFollowsAnExplicitParentImport for the chained case.
func TestResolveFacts_nearestPropsWins(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><PropertyGroup><TargetFramework>net6.0</TargetFramework></PropertyGroup></Project>`)
	write(t, root, "src/Directory.Build.props", `<Project><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want [net8.0] from src/Directory.Build.props", f.TFMs)
	}
	if len(f.PropsPaths) != 1 {
		t.Errorf("PropsPaths = %v, want only the nearest file: it does not import its parent", f.PropsPaths)
	}
}

// A conditioned PropertyGroup is not an unconditional fact; guessing which branch MSBuild takes is
// exactly the evaluation this package promises not to do.
func TestResolveFacts_conditionalPropertyIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project>
  <PropertyGroup Condition="'$(Configuration)'=='Debug'">
    <TargetFramework>net6.0</TargetFramework>
  </PropertyGroup>
  <PropertyGroup>
    <LangVersion>latest</LangVersion>
  </PropertyGroup>
</Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want only the unconditional net8.0", f.TFMs)
	}
	if f.LangVersion != "latest" {
		t.Errorf("LangVersion = %q, want latest from the unconditional group", f.LangVersion)
	}
}

// The walk must stop at the repo root: Directory.Build.props above a checkout belongs to somebody else.
func TestResolveFacts_walkStopsAtRepoRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, outer, "Directory.Build.props", `<Project><PropertyGroup><TargetFramework>net48</TargetFramework></PropertyGroup></Project>`)
	root := filepath.Join(outer, "repo")
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if len(f.TFMs) != 0 {
		t.Errorf("TFMs = %v, want none: the props file is outside the repo", f.TFMs)
	}
	if len(f.PropsPaths) != 0 {
		t.Errorf("PropsPaths = %v, want none", f.PropsPaths)
	}
}

// Directory.Build.targets is evaluated after the project, so it is read too.
func TestResolveFacts_readsDirectoryBuildTargets(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.targets", `<Project><PropertyGroup><Nullable>enable</Nullable></PropertyGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if f.Nullable != "enable" {
		t.Errorf("Nullable = %q, want enable from Directory.Build.targets", f.Nullable)
	}
}

func TestResolveFacts_multiTargeting(t *testing.T) {
	root := t.TempDir()
	csproj := write(t, root, "src/Lib/Lib.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFrameworks>netstandard2.0;net8.0</TargetFrameworks></PropertyGroup>
</Project>`)
	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"netstandard2.0", "net8.0"}) {
		t.Errorf("TFMs = %v, want both monikers in document order", f.TFMs)
	}
	if got := f.MaxNetMajor(); got != 8 {
		t.Errorf("MaxNetMajor = %d, want 8 (netstandard contributes nothing)", got)
	}
}

// Package versions: attribute form, child-element form, and central package management.
func TestResolveFacts_packageVersions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Packages.props", `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="xunit" Version="2.9.2" />
    <PackageVersion Include="Moq" Version="4.20.72" />
  </ItemGroup>
</Project>`)
	csproj := write(t, root, "tests/App.Tests/App.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="xunit" />
    <PackageReference Include="Moq" />
    <PackageReference Include="FluentAssertions" Version="6.12.0" />
    <PackageReference Include="Microsoft.NET.Test.Sdk">
      <Version>17.11.1</Version>
    </PackageReference>
  </ItemGroup>
</Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	want := map[string]string{
		"xunit":                  "2.9.2",
		"Moq":                    "4.20.72",
		"FluentAssertions":       "6.12.0",
		"Microsoft.NET.Test.Sdk": "17.11.1",
	}
	for id, version := range want {
		got, ok := f.PackageVersion(id)
		if !ok {
			t.Errorf("PackageVersion(%q): not found", id)
			continue
		}
		if got != version {
			t.Errorf("PackageVersion(%q) = %q, want %q", id, got, version)
		}
	}
	if _, ok := f.PackageVersion("NotReferenced"); ok {
		t.Error("PackageVersion(NotReferenced) resolved; a PackageVersion entry is not a reference")
	}
	// Lookup is case-insensitive: NuGet ids are.
	if v, ok := f.PackageVersion("XUNIT"); !ok || v != "2.9.2" {
		t.Errorf("PackageVersion(XUNIT) = %q,%v, want 2.9.2,true", v, ok)
	}
	if !f.CentralPackageManagement {
		t.Error("CentralPackageManagement = false, want true")
	}
}

// A versionless reference with no CPM entry is still a reference — callers need to know the package
// is there even when the version is unknown.
func TestResolveFacts_versionlessReferenceWithoutCPM(t *testing.T) {
	root := t.TempDir()
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="Serilog" /></ItemGroup>
</Project>`)
	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	v, ok := f.PackageVersion("Serilog")
	if !ok {
		t.Fatal("Serilog not reported as referenced")
	}
	if v != "" {
		t.Errorf("version = %q, want empty for an unresolvable versionless reference", v)
	}
	if !f.ReferencesPackage("serilog") {
		t.Error("ReferencesPackage(serilog) = false")
	}
	if f.ReferencesPackage("coverlet.collector") {
		t.Error("ReferencesPackage(coverlet.collector) = true, want false")
	}
}

// Commented-out references and TFMs must not register. Same reason StripXMLComments exists.
func TestResolveFacts_ignoresXMLComments(t *testing.T) {
	root := t.TempDir()
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <!-- <TargetFramework>net6.0</TargetFramework> -->
    <TargetFramework>net8.0</TargetFramework>
  </PropertyGroup>
  <ItemGroup>
    <!-- <PackageReference Include="Moq" Version="4.0.0" /> -->
  </ItemGroup>
</Project>`)
	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want [net8.0]", f.TFMs)
	}
	if f.ReferencesPackage("Moq") {
		t.Error("a commented-out PackageReference registered")
	}
}

func TestResolveFacts_missingProject(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveFacts(root, filepath.Join(root, "nope.csproj")); err == nil {
		t.Fatal("want an error for a missing project file")
	}
}
