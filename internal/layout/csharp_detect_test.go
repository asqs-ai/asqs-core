package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const unitCsprojXML = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
	`<PackageReference Include="xunit" Version="2.9.2"/>` +
	`<PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.12.0"/></ItemGroup></Project>`

const e2eCsprojXML = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
	`<PackageReference Include="xunit" Version="2.9.2"/>` +
	`<PackageReference Include="Microsoft.Playwright" Version="1.49.0"/></ItemGroup></Project>`

// Unit tests route into an existing unit-test project even when it is NOT under a tests/ root.
func TestSuggestedCSharpUnitTestPathRoutesIntoExistingProject(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, "src", "Core", "Foo.cs"), "// src")
	writeRepoFile(t, filepath.Join(repo, "MyApp.UnitTests", "MyApp.UnitTests.csproj"), unitCsprojXML)

	got := SuggestedCSharpUnitTestPath("src/Core/Foo.cs", repo)
	want := filepath.Join("MyApp.UnitTests", "Core", "FooTests.cs")
	if got != want {
		t.Fatalf("unit path = %q want %q (should route into the existing project)", got, want)
	}
}

// Unit detection must ignore E2E (Playwright) projects; E2E detection must find them.
func TestCSharpTestProjectDetectionSeparatesUnitAndE2E(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, "e2e", "App.E2E.csproj"), e2eCsprojXML)

	if got := DetectCSharpUnitTestProjectDir(repo); got != "" {
		t.Fatalf("unit detection must ignore an e2e-only repo, got %q", got)
	}
	if got := DetectCSharpE2EProjectDir(repo); got != "e2e" {
		t.Fatalf("DetectCSharpE2EProjectDir = %q want e2e", got)
	}
	// With only an e2e project, unit placement defaults to a tests/ tree — NOT into the e2e project and
	// NOT a sibling inside production (src/).
	got := SuggestedCSharpUnitTestPath("src/Core/Foo.cs", repo)
	if want := filepath.Join("tests", "Core", "FooTests.cs"); got != want {
		t.Fatalf("unit path with only an e2e project = %q want %q (never src/, never e2e)", got, want)
	}
}

// A brand-new C# unit test must NEVER be placed beside its source inside a production project, even
// when no tests/ root or test project exists yet (the CS0246 cause). It defaults to a tests/ tree.
func TestSuggestedCSharpUnitTestPathNeverSiblingInProduction(t *testing.T) {
	repo := t.TempDir() // empty: no tests/ dir, no test project
	got := SuggestedCSharpUnitTestPath("src/Core/Legacy/LegacyXmlCatalogReader.cs", repo)
	want := filepath.Join("tests", "Core", "Legacy", "LegacyXmlCatalogReaderTests.cs")
	if got != want {
		t.Fatalf("got %q want %q (must not land in src/)", got, want)
	}
	if strings.HasPrefix(filepath.ToSlash(got), "src/") {
		t.Fatalf("test must never be placed inside a production (src/) path: %q", got)
	}
}

// C# E2E placement routes under e2e/ (default), and into an existing e2e project when present.
func TestSuggestedCSharpE2ETestPath(t *testing.T) {
	repo := t.TempDir()

	got := SuggestedCSharpE2ETestPath("src/Api/UsersController.cs", repo)
	if want := filepath.Join("e2e", "Api", "UsersControllerE2ETests.cs"); got != want {
		t.Fatalf("default e2e path = %q want %q", got, want)
	}

	writeRepoFile(t, filepath.Join(repo, "test-e2e", "App.E2E.csproj"), e2eCsprojXML)
	got2 := SuggestedCSharpE2ETestPath("src/Api/UsersController.cs", repo)
	if want := filepath.Join("test-e2e", "Api", "UsersControllerE2ETests.cs"); got2 != want {
		t.Fatalf("existing-e2e-project path = %q want %q", got2, want)
	}
}
