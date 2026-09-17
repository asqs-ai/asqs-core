package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// A Web-SDK-only repository with no root .sln used to resolve no eval entry at all.
//
// discoverSDKStyleCsprojPathsForDotnet matched the exact string Sdk="Microsoft.NET.Sdk", which every
// ASP.NET Core project fails — those declare Microsoft.NET.Sdk.Web. With no SDK-style project found
// and no solution to fall back on, the compile, test and coverage steps had nothing to point at,
// while bootstrap (which matched the SDK family) had happily created a test project in the same tree.
func TestResolveDotnetEval_webSDKOnlyRepoWithoutSolution(t *testing.T) {
	repo := t.TempDir()
	writeProject(t, filepath.Join(repo, "src", "Api", "Api.csproj"), `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
</Project>`)

	paths, err := discoverSDKStyleCsprojPathsForDotnet(repo)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("discovered %v, want the one Web SDK project", paths)
	}
}

// Every SDK in the family counts, and a legacy project still does not.
func TestDiscoverSDKStyleCsproj_familyAndLegacy(t *testing.T) {
	repo := t.TempDir()
	writeProject(t, filepath.Join(repo, "src", "Api", "Api.csproj"), `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`)
	writeProject(t, filepath.Join(repo, "src", "Worker", "Worker.csproj"), `<Project Sdk="Microsoft.NET.Sdk.Worker"></Project>`)
	writeProject(t, filepath.Join(repo, "src", "Core", "Core.csproj"), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	writeProject(t, filepath.Join(repo, "src", "Old", "Old.csproj"), `<Project ToolsVersion="15.0">
  <Import Project="$(MSBuildToolsPath)\Microsoft.CSharp.targets" />
</Project>`)

	paths, err := discoverSDKStyleCsprojPathsForDotnet(repo)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("discovered %d project(s) %v, want 3 (Web, Worker, bare) and not the legacy one", len(paths), paths)
	}
	for _, p := range paths {
		if filepath.Base(p) == "Old.csproj" {
			t.Fatalf("legacy non-SDK project %s was accepted", p)
		}
	}
}

func writeProject(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
