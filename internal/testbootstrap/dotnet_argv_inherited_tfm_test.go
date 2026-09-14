package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBootstrapTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Run api-6ce78962b97320360f8c75480489fef8 is the case. A Clean Architecture solution declares
// net10.0 once, in Directory.Build.props; the bootstrap profile read that correctly and reported
// net10.0, and the smoke build was then handed /p:TargetFramework=net8.0 from the configured
// fallback because the check behind it looked at the .csproj file alone. The project restores for
// net10.0, so:
//
//	error NETSDK1005: Assets file '…/obj/project.assets.json' doesn't have a target for 'net8.0'
//
// test_bootstrap.verify_failed aborts the run, so the whole repository got nothing — no gap, no
// test, no report beyond the failure itself.
func TestAppendDotnetCLIArgsTFMFallback_leavesAnInheritedFrameworkAlone(t *testing.T) {
	root := writeBootstrapTree(t, map[string]string{
		"Directory.Build.props": `<Project><PropertyGroup>` +
			`<TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>`,
		"tests/App.FunctionalTests/App.FunctionalTests.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	csproj := filepath.Join(root, "tests", "App.FunctionalTests", "App.FunctionalTests.csproj")
	argv := []string{"dotnet", "build", "tests/App.FunctionalTests/App.FunctionalTests.csproj", "--verbosity", "quiet"}

	got := appendDotnetCLIArgsTFMFallback(root, argv, csproj, "net8.0")
	if joined := strings.Join(got, " "); strings.Contains(joined, "/p:TargetFramework=") {
		t.Fatalf("pinned a framework over the one the project inherits: %s", joined)
	}
}

// The fallback still does its job for the project it exists for: one that evaluates to no framework
// at all, in its own file or anything it inherits.
func TestAppendDotnetCLIArgsTFMFallback_stillPinsWhenNothingDeclaresOne(t *testing.T) {
	root := writeBootstrapTree(t, map[string]string{
		"Directory.Build.props":                                `<Project><PropertyGroup><Nullable>enable</Nullable></PropertyGroup></Project>`,
		"tests/App.FunctionalTests/App.FunctionalTests.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	csproj := filepath.Join(root, "tests", "App.FunctionalTests", "App.FunctionalTests.csproj")
	argv := []string{"dotnet", "build", "tests/App.FunctionalTests/App.FunctionalTests.csproj"}

	got := appendDotnetCLIArgsTFMFallback(root, argv, csproj, "net8.0")
	if len(got) < 3 || got[2] != "/p:TargetFramework=net8.0" {
		t.Fatalf("the fallback did not pin where nothing declares a framework: %v", got)
	}
}
