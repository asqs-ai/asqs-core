package dotnetproj

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func solutionRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/Shop.Core/Shop.Core.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	write("src/Shop.Data/Shop.Data.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><ProjectReference Include="..\Shop.Core\Shop.Core.csproj" /></ItemGroup>
</Project>`)
	write("src/Shop/Shop.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup><ProjectReference Include="..\Shop.Core\Shop.Core.csproj" /></ItemGroup>
</Project>`)
	write("tests/Shop.Tests/Shop.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="NUnit" Version="4.2.2" />
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
  </ItemGroup>
  <ItemGroup><ProjectReference Include="..\..\src\Shop.Data\Shop.Data.csproj" /></ItemGroup>
</Project>`)
	return repo
}

// A test cannot name a type it cannot reference, however well it is written. The §4.1.1 validation
// run discarded four of nine gaps for exactly this — the test project referenced Shop.Core and not
// Shop — and nothing noticed until the compiler did, after the gap had spent its generation and its
// fix rounds.
func TestTestProjectReachableDirs(t *testing.T) {
	repo := solutionRepo(t)
	got := TestProjectReachableDirs(repo, "tests/Shop.Tests/Shop.Tests.csproj")
	want := []string{"src/Shop.Core", "src/Shop.Data", "tests/Shop.Tests"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reachable = %v, want %v (transitive through Shop.Data)", got, want)
	}
	// Shop is a sibling nothing references: its controllers are unreachable from this test project.
	for _, d := range got {
		if d == "src/Shop" {
			t.Errorf("src/Shop is reachable, but no ProjectReference chain reaches it: %v", got)
		}
	}
}

// An unreadable project is "no claim", not "nothing is reachable". Filtering every gap out because
// a file could not be parsed would turn a parse error into a run that plans nothing.
func TestTestProjectReachableDirs_noClaimWithoutAProject(t *testing.T) {
	repo := solutionRepo(t)
	for _, rel := range []string{"", "tests/Missing/Missing.csproj"} {
		if got := TestProjectReachableDirs(repo, rel); got != nil {
			t.Errorf("TestProjectReachableDirs(%q) = %v, want nil", rel, got)
		}
	}
	if got := TestProjectReachableDirs("", "a.csproj"); got != nil {
		t.Errorf("no repo root = %v, want nil", got)
	}
}

// What makes a project a test project is that it references a test framework, not where it sits.
func TestFindTestProjects(t *testing.T) {
	repo := solutionRepo(t)
	got := FindTestProjects(repo)
	if len(got) != 1 || got[0] != "tests/Shop.Tests/Shop.Tests.csproj" {
		t.Fatalf("FindTestProjects = %v, want [tests/Shop.Tests/Shop.Tests.csproj]", got)
	}
}

func TestPathIsReachableFrom(t *testing.T) {
	dirs := []string{"src/Shop.Core", "tests/Shop.Tests"}
	for _, p := range []string{"src/Shop.Core/Basket.cs", "src/Shop.Core/Models/Order.cs", "tests/Shop.Tests/T.cs"} {
		if !PathIsReachableFrom(p, dirs) {
			t.Errorf("PathIsReachableFrom(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"src/Shop/Controllers/Home.cs", "src/Shop.CoreExtras/X.cs", ""} {
		if PathIsReachableFrom(p, dirs) {
			t.Errorf("PathIsReachableFrom(%q) = true, want false", p)
		}
	}
	// A project at the repository root owns everything.
	if !PathIsReachableFrom("src/anything.cs", []string{""}) {
		t.Error("a root project should reach every path")
	}
	if PathIsReachableFrom("a.cs", nil) {
		t.Error("no projects should reach nothing")
	}
}
