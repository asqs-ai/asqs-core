package evaluator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGlobalUsingsRepo(t *testing.T, files map[string]string) string {
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

const guFixtureProj = `<Project Sdk="Microsoft.NET.Sdk"></Project>`

// A C# project that declares its imports with `global using` leaves individual files carrying no
// using line for those namespaces — so the file under repair says nothing about where its types
// come from, and neither does any file the diagnostic names.
//
// Run api-8d5367b3383017e25f09e53dacf6c275 is the bill. CreateContributorHandler takes
// IRepository<Contributor>, which that project's global usings name; its own file imports only the
// aggregate namespace. evaluator.fix_api_surface_unavailable fired on all eleven fix rounds, with
// symbol:IRepository as the target in eight of them, and the loop stopped on no progress at
// iteration 10 of 20.
func TestCSharpGlobalUsingsFilesFor_findsTheOwningProjectsDeclarations(t *testing.T) {
	root := writeGlobalUsingsRepo(t, map[string]string{
		"src/App.UseCases/App.UseCases.csproj": guFixtureProj,
		"src/App.UseCases/GlobalUsings.cs":     "global using Ardalis.SharedKernel;\nglobal using Ardalis.Result;\n",
		"src/App.UseCases/Handlers/CreateHandler.cs": "using App.Core.Aggregate;\n" +
			"namespace App.UseCases.Handlers;\npublic class CreateHandler { }\n",
		// A sibling project's global usings must not be dragged in.
		"src/App.Web/App.Web.csproj":  guFixtureProj,
		"src/App.Web/GlobalUsings.cs": "global using FastEndpoints;\n",
	})

	got := csharpGlobalUsingsFilesFor(root, "csharp", []string{"src/App.UseCases/Handlers/CreateHandler.cs"})
	if len(got) != 1 || got[0] != "src/App.UseCases/GlobalUsings.cs" {
		t.Fatalf("want the owning project's global usings only, got %v", got)
	}
}

// The file need not be called GlobalUsings.cs; the declaration is what matters.
func TestCSharpGlobalUsingsFilesFor_keysOnTheDeclarationNotTheFilename(t *testing.T) {
	root := writeGlobalUsingsRepo(t, map[string]string{
		"src/App/App.csproj": guFixtureProj,
		"src/App/Usings.cs":  "global using Ardalis.Result;\n",
		// An ordinary file of plain usings declares nothing project-wide and must not be returned.
		"src/App/Helpers.cs": "using System.Text;\nnamespace App;\npublic class Helpers { }\n",
		"src/App/Thing.cs":   "namespace App;\npublic class Thing { }\n",
	})

	got := csharpGlobalUsingsFilesFor(root, "csharp", []string{"src/App/Thing.cs"})
	if len(got) != 1 || got[0] != "src/App/Usings.cs" {
		t.Fatalf("want the file that declares global usings, got %v", got)
	}
}

// Every other language, and every repository without the construct, must get nothing: this is a C#
// idiom and the cost of returning files anyway is prompt budget taken from the artifacts.
func TestCSharpGlobalUsingsFilesFor_silentWhereItDoesNotApply(t *testing.T) {
	root := writeGlobalUsingsRepo(t, map[string]string{
		"src/App/App.csproj":      guFixtureProj,
		"src/App/GlobalUsings.cs": "global using Ardalis.Result;\n",
		"src/App/Thing.cs":        "namespace App;\npublic class Thing { }\n",
	})
	for _, lang := range []string{"java", "typescript", "javascript", "go", ""} {
		if got := csharpGlobalUsingsFilesFor(root, lang, []string{"src/App/Thing.cs"}); len(got) != 0 {
			t.Errorf("%s: want nothing, got %v", lang, got)
		}
	}
	if got := csharpGlobalUsingsFilesFor("", "csharp", []string{"src/App/Thing.cs"}); len(got) != 0 {
		t.Errorf("with no repo path, want nothing, got %v", got)
	}

	noGlobals := writeGlobalUsingsRepo(t, map[string]string{
		"src/App/App.csproj": guFixtureProj,
		"src/App/Thing.cs":   "using System;\nnamespace App;\npublic class Thing { }\n",
	})
	if got := csharpGlobalUsingsFilesFor(noGlobals, "csharp", []string{"src/App/Thing.cs"}); len(got) != 0 {
		t.Errorf("a project with no global usings must yield nothing, got %v", got)
	}
}

// One entry per project however many files the round is repairing, and the file already in the
// prompt is never returned twice.
func TestCSharpGlobalUsingsFilesFor_dedupesAcrossFilesAndProjects(t *testing.T) {
	root := writeGlobalUsingsRepo(t, map[string]string{
		"src/App/App.csproj":               guFixtureProj,
		"src/App/GlobalUsings.cs":          "global using Ardalis.Result;\n",
		"src/App/A.cs":                     "namespace App;\npublic class A { }\n",
		"src/App/B.cs":                     "namespace App;\npublic class B { }\n",
		"tests/App.Tests/App.Tests.csproj": guFixtureProj,
		"tests/App.Tests/GlobalUsings.cs":  "global using Xunit;\n",
		"tests/App.Tests/ATests.cs":        "namespace App.Tests;\npublic class ATests { }\n",
	})

	got := csharpGlobalUsingsFilesFor(root, "csharp", []string{
		"src/App/A.cs", "src/App/B.cs", "tests/App.Tests/ATests.cs",
	})
	want := map[string]bool{"src/App/GlobalUsings.cs": true, "tests/App.Tests/GlobalUsings.cs": true}
	if len(got) != 2 {
		t.Fatalf("want one per project, got %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected %q", g)
		}
	}

	// A global-usings file already in the prompt is not offered again.
	if got := csharpGlobalUsingsFilesFor(root, "csharp", []string{"src/App/GlobalUsings.cs"}); len(got) != 0 {
		t.Errorf("want nothing when the global usings file is itself in scope, got %v", got)
	}
}
