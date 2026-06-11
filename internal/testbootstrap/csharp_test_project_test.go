package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/layout"
)

func writeCsproj(t *testing.T, path, tfm string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>` + tfm + `</TargetFramework>
  </PropertyGroup>
</Project>
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDedicatedCSharpTestProjectRoutesTestsOutOfProduction proves the fix: with production projects and
// no test project, bootstrap creates one dedicated xUnit project under tests/, and the layout then
// routes generated tests there instead of into the production projects (the CS0246 cause).
func TestDedicatedCSharpTestProjectRoutesTestsOutOfProduction(t *testing.T) {
	repo := t.TempDir()
	writeCsproj(t, filepath.Join(repo, "src", "Core", "Core.csproj"), "net8.0")
	writeCsproj(t, filepath.Join(repo, "src", "App", "App.csproj"), "net6.0") // lower TFM → max wins

	prod, test, err := splitCSharpProdAndTestCsprojs(repo)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(prod) != 2 || len(test) != 0 {
		t.Fatalf("expected 2 prod / 0 test, got %d/%d", len(prod), len(test))
	}

	// A test must never be placed inside the production project (src/) — even before the dedicated
	// project is created, placement defaults to a tests/ tree.
	if got := layout.SuggestedCSharpUnitTestPath("src/Core/Foo.cs", repo); strings.HasPrefix(filepath.ToSlash(got), "src/") {
		t.Fatalf("test must not be placed inside production (src/): %q", got)
	}

	testProj, changed, err := createDedicatedCSharpTestProject(repo, repo, prod, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("expected changed files")
	}
	wantPath := filepath.Join(repo, "tests", filepath.Base(repo)+".Tests.csproj")
	if testProj != wantPath {
		t.Fatalf("test project path = %q, want %q", testProj, wantPath)
	}

	b, err := os.ReadFile(testProj)
	if err != nil {
		t.Fatalf("read test project: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`Include="xunit"`,
		"Microsoft.NET.Test.Sdk",
		"<TargetFramework>net8.0</TargetFramework>", // max(net8, net6)
		`Include="../src/App/App.csproj"`,
		`Include="../src/Core/Core.csproj"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("generated test project missing %q\n---\n%s", want, s)
		}
	}

	// After the fix: layout detects the tests/ root and routes tests there, out of the production project.
	got := layout.SuggestedCSharpUnitTestPath("src/Core/Foo.cs", repo)
	if !strings.HasPrefix(filepath.ToSlash(got), "tests/") {
		t.Fatalf("after fix: expected test under tests/, got %q", got)
	}
	if strings.Contains(filepath.ToSlash(got), "src/") {
		t.Fatalf("after fix: test must not land in a production (src/) path, got %q", got)
	}

	// Idempotent: a second call makes no changes and returns the same path.
	again, changed2, err := createDedicatedCSharpTestProject(repo, repo, prod, "")
	if err != nil || again != testProj || len(changed2) != 0 {
		t.Fatalf("idempotency: again=%q changed=%v err=%v", again, changed2, err)
	}
}

// TestMigrateStrayCSharpTestsIntoTestRoot proves that a test file left inside a production project
// (the CS0246 cause) is relocated into the dedicated test project, mirrored.
func TestMigrateStrayCSharpTestsIntoTestRoot(t *testing.T) {
	repo := t.TempDir()
	writeCsproj(t, filepath.Join(repo, "src", "Core", "Core.csproj"), "net8.0")
	stray := filepath.Join(repo, "src", "Core", "Legacy", "LegacyXmlCatalogReaderTests.cs")
	if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("using Xunit;\npublic class LegacyXmlCatalogReaderTests { [Fact] public void T(){} }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-test production file with a similar name must NOT be moved.
	keep := filepath.Join(repo, "src", "Core", "Legacy", "LegacyXmlCatalogReader.cs")
	if err := os.WriteFile(keep, []byte("public class LegacyXmlCatalogReader {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prod, _, err := splitCSharpProdAndTestCsprojs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := createDedicatedCSharpTestProject(repo, repo, prod, ""); err != nil {
		t.Fatal(err)
	}

	moved := migrateStrayCSharpTestsIntoTestRoot(repo, "tests", prod)
	if len(moved) != 1 {
		t.Fatalf("expected exactly 1 moved test, got %v", moved)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray test must be removed from the production project, still present: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("production source must be left in place, got: %v", err)
	}
	target := filepath.Join(repo, "tests", "Core", "Legacy", "LegacyXmlCatalogReaderTests.cs")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stray test must be relocated to %q: %v", target, err)
	}
}

// TestDedicatedCSharpE2EProjectIsSeparateAndPlaywright proves the E2E project is created under a
// SEPARATE root (e2e/, no glob conflict with tests/), carries Playwright, and is distinguishable from
// the unit project by detection.
func TestDedicatedCSharpE2EProjectIsSeparateAndPlaywright(t *testing.T) {
	repo := t.TempDir()
	writeCsproj(t, filepath.Join(repo, "src", "Api", "Api.csproj"), "net8.0")
	prod, _, err := splitCSharpProdAndTestCsprojs(repo)
	if err != nil || len(prod) != 1 {
		t.Fatalf("split: %v len=%d", err, len(prod))
	}

	e2eProj, changed, err := createDedicatedCSharpE2EProject(repo, repo, prod, "")
	if err != nil || len(changed) == 0 {
		t.Fatalf("create e2e: %v changed=%v", err, changed)
	}
	want := filepath.Join(repo, "e2e", dedicatedCSharpProjectBaseName(repo)+".E2E.csproj")
	if e2eProj != want {
		t.Fatalf("e2e project = %q want %q", e2eProj, want)
	}
	b, _ := os.ReadFile(e2eProj)
	s := string(b)
	for _, w := range []string{"Microsoft.Playwright", `Include="xunit"`, "Microsoft.NET.Test.Sdk", `Include="../src/Api/Api.csproj"`} {
		if !strings.Contains(s, w) {
			t.Fatalf("e2e project missing %q\n%s", w, s)
		}
	}

	unitProj, _, err := createDedicatedCSharpTestProject(repo, repo, prod, "")
	if err != nil {
		t.Fatalf("create unit: %v", err)
	}
	if filepath.Dir(unitProj) == filepath.Dir(e2eProj) {
		t.Fatalf("unit and e2e must live in different dirs (glob safety): %q vs %q", unitProj, e2eProj)
	}
	if got := layout.DetectCSharpE2EProjectDir(repo); got != "e2e" {
		t.Fatalf("DetectCSharpE2EProjectDir = %q want e2e", got)
	}
	if got := layout.DetectCSharpUnitTestProjectDir(repo); got != "tests" {
		t.Fatalf("DetectCSharpUnitTestProjectDir = %q want tests (must exclude the e2e project)", got)
	}
}
