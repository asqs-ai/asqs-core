package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// The multi-target pin exists so `dotnet test` in a Linux container does not try to build a net48
// target. A project that declares TargetFrameworks in Directory.Build.props — the layout that makes
// multi-targeting maintainable across a solution — read as declaring none, so no pin was applied and
// the step failed on the .NET Framework target it could never build.
func TestParseCsprojTargetFrameworksList_readsDirectoryBuildProps(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"),
		`<Project><PropertyGroup><TargetFrameworks>net48;net8.0</TargetFrameworks></PropertyGroup></Project>`)
	csproj := filepath.Join(repo, "src", "Lib", "Lib.csproj")
	mustWriteFile(t, csproj, `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	tfms, err := ParseCsprojTargetFrameworksList(csproj)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tfms) != 2 || tfms[0] != "net48" || tfms[1] != "net8.0" {
		t.Fatalf("TFMs = %v, want [net48 net8.0] inherited from Directory.Build.props", tfms)
	}
	if got, ok := PickDotnetMultiTargetTestTargetFramework(csproj, ""); !ok || got != "net8.0" {
		t.Fatalf("pin = %q,%v, want net8.0,true", got, ok)
	}
}

// The project's own declaration still wins over the inherited one.
func TestParseCsprojTargetFrameworksList_projectWins(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "Directory.Build.props"),
		`<Project><PropertyGroup><TargetFramework>net6.0</TargetFramework></PropertyGroup></Project>`)
	csproj := filepath.Join(repo, "src", "Lib", "Lib.csproj")
	mustWriteFile(t, csproj, `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)

	tfms, err := ParseCsprojTargetFrameworksList(csproj)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tfms) != 1 || tfms[0] != "net8.0" {
		t.Fatalf("TFMs = %v, want [net8.0]", tfms)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
