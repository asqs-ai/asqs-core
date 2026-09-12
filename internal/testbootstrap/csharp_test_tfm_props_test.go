package testbootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// The test project's TFM comes from the production projects, and those inherit from
// Directory.Build.props. inferCSharpTestTFM read each .csproj in isolation, so a repository that
// factors TargetFramework out got the net8.0 fallback — silently correct on a net8 repo and
// silently wrong on every other, where the test project then failed to reference the SUT.
func TestInferCSharpTestTFM_readsDirectoryBuildProps(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "Directory.Build.props"), `<Project><PropertyGroup>
  <TargetFramework>net9.0</TargetFramework>
</PropertyGroup></Project>`)
	app := filepath.Join(repo, "src", "App", "App.csproj")
	mustWrite(t, app, `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`)

	if got := inferCSharpTestTFM(repo, []string{app}, ""); got != "net9.0" {
		t.Fatalf("inferCSharpTestTFM = %q, want net9.0 from Directory.Build.props", got)
	}
}

// A TFM in the project still wins, and the highest across projects is chosen.
func TestInferCSharpTestTFM_projectWinsAndHighestAcrossProjects(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "Directory.Build.props"), `<Project><PropertyGroup>
  <TargetFramework>net6.0</TargetFramework>
</PropertyGroup></Project>`)
	app := filepath.Join(repo, "src", "App", "App.csproj")
	mustWrite(t, app, `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	core := filepath.Join(repo, "src", "Core", "Core.csproj")
	mustWrite(t, core, `<Project Sdk="Microsoft.NET.Sdk"></Project>`) // inherits net6.0

	if got := inferCSharpTestTFM(repo, []string{app, core}, ""); got != "net8.0" {
		t.Fatalf("inferCSharpTestTFM = %q, want net8.0 (highest across projects)", got)
	}
}

// With nothing declared anywhere the configured fallback applies, then the built-in default.
func TestInferCSharpTestTFM_fallbacks(t *testing.T) {
	repo := t.TempDir()
	lib := filepath.Join(repo, "Lib.csproj")
	mustWrite(t, lib, `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	if got := inferCSharpTestTFM(repo, []string{lib}, "net7.0"); got != "net7.0" {
		t.Fatalf("inferCSharpTestTFM = %q, want the configured fallback net7.0", got)
	}
	if got := inferCSharpTestTFM(repo, []string{lib}, ""); got != "net8.0" {
		t.Fatalf("inferCSharpTestTFM = %q, want the built-in default net8.0", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
