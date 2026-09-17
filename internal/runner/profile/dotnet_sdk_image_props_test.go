package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// global.json is what the repo says its SDK must be, and the eval image has to satisfy it: a
// net8.0 project pinned to SDK 10 cannot restore under sdk:8.0 ("A compatible .NET SDK was not
// found"). The image was chosen from TFMs alone, so global.json was parsed and then ignored by
// everything except the Playwright side-by-side install.
func TestResolveDotNetDockerImage_honoursGlobalJson(t *testing.T) {
	repo := t.TempDir()
	writeAt(t, filepath.Join(repo, "src", "App", "App.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	writeAt(t, filepath.Join(repo, "global.json"), `{"sdk":{"version":"10.0.100","rollForward":"latestMajor"}}`)

	if got := resolveDotNetDockerImage("", repo); got != "mcr.microsoft.com/dotnet/sdk:10.0" {
		t.Fatalf("image = %q, want sdk:10.0 (global.json pins SDK 10)", got)
	}
}

// The higher of the two wins in the other direction too: net9.0 projects under an SDK 8 pin still
// need an SDK that can build net9.0.
func TestResolveDotNetDockerImage_tfmHigherThanGlobalJson(t *testing.T) {
	repo := t.TempDir()
	writeAt(t, filepath.Join(repo, "src", "App", "App.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net9.0</TargetFramework></PropertyGroup></Project>`)
	writeAt(t, filepath.Join(repo, "global.json"), `{"sdk":{"version":"8.0.404"}}`)

	if got := resolveDotNetDockerImage("", repo); got != "mcr.microsoft.com/dotnet/sdk:9.0" {
		t.Fatalf("image = %q, want sdk:9.0", got)
	}
}

// A TFM inherited from Directory.Build.props counts: the walk used to read .csproj files only, so a
// repo that factors the moniker out looked like it declared none and took the default image.
func TestResolveDotNetDockerImage_tfmFromDirectoryBuildProps(t *testing.T) {
	repo := t.TempDir()
	writeAt(t, filepath.Join(repo, "Directory.Build.props"),
		`<Project><PropertyGroup><TargetFramework>net9.0</TargetFramework></PropertyGroup></Project>`)
	writeAt(t, filepath.Join(repo, "src", "App", "App.csproj"), `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`)

	if got := resolveDotNetDockerImage("", repo); got != "mcr.microsoft.com/dotnet/sdk:9.0" {
		t.Fatalf("image = %q, want sdk:9.0 from Directory.Build.props", got)
	}
}

// An explicit image always wins, and a repo with nothing to go on takes the default.
func TestResolveDotNetDockerImage_configuredAndDefault(t *testing.T) {
	repo := t.TempDir()
	if got := resolveDotNetDockerImage("mirror.local/sdk:8.0", repo); got != "mirror.local/sdk:8.0" {
		t.Fatalf("image = %q, want the configured override", got)
	}
	if got := resolveDotNetDockerImage("", repo); got != DefaultDotNetImage {
		t.Fatalf("image = %q, want the default %q", got, DefaultDotNetImage)
	}
}
