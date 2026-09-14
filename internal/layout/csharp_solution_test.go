package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSolutionTree(t *testing.T, files map[string]string) string {
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

const xunitCsproj = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
	`<PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.12.0" />` +
	`<PackageReference Include="xunit" Version="2.9.2" /></ItemGroup></Project>`

// Where a generated test LANDS decides whether it ever runs. The evaluator builds and tests the
// solution, so a project outside it is a project whose tests are never compiled and never executed —
// and the run still reports compile=ok, because the solution it compiled never saw them.
//
// Run api-4198e8aa94b1aad506e17a3a6ff8f74b wrote all ten of its unit tests into
// tests/Clean.Architecture.AspireTests, which no `dotnet test <solution>` invocation touched. Every
// candidate scored identically — under a tests/ root, one level deep — so the winner was whichever
// the directory walk reached first, and "Aspire" sorts before "Functional", "Integration" and
// "Unit".
func TestDetectCSharpUnitTestProjectDir_prefersAProjectTheSolutionLists(t *testing.T) {
	files := map[string]string{
		"App.slnx": `<Solution>` +
			`<Project Path="tests/App.UnitTests/App.UnitTests.csproj" />` +
			`<Project Path="src/App/App.csproj" /></Solution>`,
		"src/App/App.csproj":                           `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"tests/App.AspireTests/App.AspireTests.csproj": xunitCsproj,
		"tests/App.UnitTests/App.UnitTests.csproj":     xunitCsproj,
	}
	root := writeSolutionTree(t, files)
	if got := DetectCSharpUnitTestProjectDir(root); got != "tests/App.UnitTests" {
		t.Fatalf("DetectCSharpUnitTestProjectDir = %q; want the project the solution lists", got)
	}
}

// With no solution to consult, the previous behaviour stands: both are equally plausible and the
// scoring decides.
func TestDetectCSharpUnitTestProjectDir_withoutASolutionKeepsScoring(t *testing.T) {
	root := writeSolutionTree(t, map[string]string{
		"tests/App.UnitTests/App.UnitTests.csproj": xunitCsproj,
	})
	if got := DetectCSharpUnitTestProjectDir(root); got != "tests/App.UnitTests" {
		t.Fatalf("DetectCSharpUnitTestProjectDir = %q; want tests/App.UnitTests", got)
	}
}

// The bootstrap already picked a project, verified the stack on it and added the packages a
// generated test needs to THAT project. Generation re-deriving its own answer is how the two came
// to disagree, so the contract wins outright when it names one.
func TestDetectCSharpUnitTestProjectDir_prefersTheProjectBootstrapVerified(t *testing.T) {
	root := writeSolutionTree(t, map[string]string{
		"App.slnx": `<Solution>` +
			`<Project Path="tests/App.AspireTests/App.AspireTests.csproj" />` +
			`<Project Path="tests/App.FunctionalTests/App.FunctionalTests.csproj" /></Solution>`,
		"tests/App.AspireTests/App.AspireTests.csproj":         xunitCsproj,
		"tests/App.FunctionalTests/App.FunctionalTests.csproj": xunitCsproj,
		".asqs/test-stack.json": `{"version":1,"language":"csharp","runner":"xunit",` +
			`"test_project":"tests/App.FunctionalTests/App.FunctionalTests.csproj","verified":true}`,
	})
	if got := DetectCSharpUnitTestProjectDir(root); got != "tests/App.FunctionalTests" {
		t.Fatalf("DetectCSharpUnitTestProjectDir = %q; want the project the bootstrap verified", got)
	}
}
