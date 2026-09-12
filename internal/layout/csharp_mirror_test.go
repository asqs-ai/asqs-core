package layout

import (
	"os"
	"path/filepath"
	"testing"
)

// csharpSolutionRepo is the ordinary multi-project layout: two source projects under src/ and a test
// project under tests/, which is what every `dotnet new sln` scaffold produces.
func csharpSolutionRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, rel := range []string{
		"src/Shop/Shop.csproj",
		"src/Shop/Pages/Orders/Index.cshtml.cs",
		"src/Shop/Controllers/HomeController.cs",
		"src/Shop.Core/Shop.Core.csproj",
		"src/Shop.Core/Services/PricingService.cs",
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTestCsproj(t, repo, "tests/Shop.Tests/Shop.Tests.csproj")
	return repo
}

// writeTestCsproj writes a project a detector will recognise: what makes a .csproj a TEST project is
// that it references a test framework, not where it sits.
func writeTestCsproj(t *testing.T, repo, rel string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
    <PackageReference Include="NUnit" Version="4.2.2" />
  </ItemGroup>
</Project>`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The mirror stripped only `src/`, so a source project's own directory name survived into the test
// tree: src/Shop/Pages/Orders became tests/Shop.Tests/Shop/Pages/Orders.
//
// In C# a folder is a namespace, and the test project's root namespace here is Shop.Tests — so that
// directory is the namespace Shop.Tests.Shop, and the segment `Shop` then SHADOWS the top-level
// Shop namespace for every file in the project. A validation run is the case: after one
// generated Razor test landed, the repository's own pre-existing PricingServiceTests.cs stopped
// compiling with "the type or namespace name 'Services' does not exist in the namespace
// 'Shop.Tests.Shop.Core'", and no edit to that file could fix it — the shadow comes from a
// different file's namespace.
//
// The mirror is therefore taken relative to the SOURCE PROJECT's directory, which is what the C#
// convention means by mirroring in the first place.
func TestSuggestedCSharpUnitTestPath_mirrorsBelowTheSourceProject(t *testing.T) {
	repo := csharpSolutionRepo(t)
	cases := []struct {
		source string
		want   string
	}{
		{"src/Shop/Pages/Orders/Index.cshtml.cs", "tests/Shop.Tests/Pages/Orders/Index.cshtmlTests.cs"},
		{"src/Shop/Controllers/HomeController.cs", "tests/Shop.Tests/Controllers/HomeControllerTests.cs"},
		{"src/Shop.Core/Services/PricingService.cs", "tests/Shop.Tests/Services/PricingServiceTests.cs"},
	}
	for _, tc := range cases {
		got := filepath.ToSlash(SuggestedCSharpUnitTestPath(tc.source, repo))
		if got != tc.want {
			t.Errorf("SuggestedCSharpUnitTestPath(%s) = %s, want %s", tc.source, got, tc.want)
		}
	}
}

// A file sitting at the source project's own root mirrors to no subdirectory at all.
func TestSuggestedCSharpUnitTestPath_projectRootFileNeedsNoMirror(t *testing.T) {
	repo := csharpSolutionRepo(t)
	p := filepath.Join(repo, "src", "Shop", "Program.cs")
	if err := os.WriteFile(p, []byte("// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := filepath.ToSlash(SuggestedCSharpUnitTestPath("src/Shop/Program.cs", repo))
	if got != "tests/Shop.Tests/ProgramTests.cs" {
		t.Errorf("SuggestedCSharpUnitTestPath = %s, want tests/Shop.Tests/ProgramTests.cs", got)
	}
}

// A repository with no .csproj above the source file keeps the old behaviour: there is no project
// directory to mirror against, and inventing one would be worse than the flat layout.
func TestSuggestedCSharpUnitTestPath_noProjectFallsBackToTheSrcStrip(t *testing.T) {
	repo := t.TempDir()
	p := filepath.Join(repo, "src", "Widgets", "Gadget.cs")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestCsproj(t, repo, "tests/App.Tests/App.Tests.csproj")
	got := filepath.ToSlash(SuggestedCSharpUnitTestPath("src/Widgets/Gadget.cs", repo))
	if got != "tests/App.Tests/Widgets/GadgetTests.cs" {
		t.Errorf("SuggestedCSharpUnitTestPath = %s, want tests/App.Tests/Widgets/GadgetTests.cs", got)
	}
}
