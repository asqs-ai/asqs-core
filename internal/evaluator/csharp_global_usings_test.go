package evaluator

import (
	"os"
	"path/filepath"
	"strings"
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

// The regression from run api-aace45f1f78d36a4474c06ad7d7ffae3, which this helper caused.
//
// The fix prompt of a monorepo round carries files from every application in it — one clamp round
// dropped four MinimalClean and seven sample files — and the helper was handed that whole map. It
// collected a project for each, sorted them, and took the first four. "MinimalClean" < "sample" <
// "src" < "tests", so the cap kept exactly the wrong four and no root-tree project was ever reached.
//
// The fixer was then shown the SAMPLE tree's global usings, wrote `using NimblePros.SharedKernel;`
// into a root-tree test, and got CS0234 'SharedKernel' does not exist in the namespace 'NimblePros'.
// Nine rounds oscillated between that and the CS0246 it started with, then the loop gave up on no
// progress. Before this helper existed the fixer guessed; with it, it was confidently wrong.
func TestCSharpGlobalUsingsFilesFor_prefersTheProjectsUnderRepair(t *testing.T) {
	root := writeGlobalUsingsRepo(t, map[string]string{
		// The root application: the one being repaired, and the one with the answer.
		"src/Clean.Architecture.Core/Clean.Architecture.Core.csproj":                         guFixtureProj,
		"src/Clean.Architecture.Core/GlobalUsings.cs":                                        "global using Ardalis.SharedKernel;\nglobal using Mediator;\n",
		"src/Clean.Architecture.Core/EventDispatcher.cs":                                     "namespace Clean.Architecture.Core;\npublic class EventDispatcher { }\n",
		"tests/Clean.Architecture.FunctionalTests/Clean.Architecture.FunctionalTests.csproj": guFixtureProj,
		"tests/Clean.Architecture.FunctionalTests/GlobalUsings.cs":                           "global using Xunit;\n",
		"tests/Clean.Architecture.FunctionalTests/EventDispatcherTests.cs":                   "namespace Clean.Architecture.FunctionalTests;\npublic class EventDispatcherTests { }\n",

		// Two sibling applications whose paths sort FIRST and whose namespaces are wrong here.
		"MinimalClean/src/MinimalClean.Architecture.Web/MinimalClean.Architecture.Web.csproj": guFixtureProj,
		"MinimalClean/src/MinimalClean.Architecture.Web/GlobalUsings.cs":                      "global using MinimalClean.Stuff;\n",
		"MinimalClean/src/MinimalClean.Architecture.Web/EventDispatcher.cs":                   "namespace MinimalClean;\npublic class EventDispatcher { }\n",
		"sample/src/NimblePros.SampleToDo.Core/NimblePros.SampleToDo.Core.csproj":             guFixtureProj,
		"sample/src/NimblePros.SampleToDo.Core/GlobalUsings.cs":                               "global using NimblePros.SharedKernel;\n",
		"sample/src/NimblePros.SampleToDo.Core/EventDispatcher.cs":                            "namespace NimblePros.SampleToDo.Core;\npublic class EventDispatcher { }\n",
	})

	// The order the caller must supply: what the round is repairing, then what it is repairing it
	// against, then whatever else the prompt happens to be carrying.
	scope := []string{
		"tests/Clean.Architecture.FunctionalTests/EventDispatcherTests.cs",
		"src/Clean.Architecture.Core/EventDispatcher.cs",
		"MinimalClean/src/MinimalClean.Architecture.Web/EventDispatcher.cs",
		"sample/src/NimblePros.SampleToDo.Core/EventDispatcher.cs",
	}
	got := csharpGlobalUsingsFilesFor(root, "csharp", scope)

	if len(got) == 0 {
		t.Fatal("no global usings returned at all")
	}
	if got[0] != "tests/Clean.Architecture.FunctionalTests/GlobalUsings.cs" {
		t.Errorf("the project under repair must come first, got %v", got)
	}
	var hasRootSource bool
	for _, g := range got {
		if g == "src/Clean.Architecture.Core/GlobalUsings.cs" {
			hasRootSource = true
		}
	}
	if !hasRootSource {
		t.Errorf("the source project's global usings — the file naming Ardalis.SharedKernel — must survive the cap, got %v", got)
	}
	// And the wrong tree must not outrank either of them.
	for i, g := range got {
		if strings.HasPrefix(g, "MinimalClean/") || strings.HasPrefix(g, "sample/") {
			if i < 2 {
				t.Errorf("a sibling application's global usings outranked the projects under repair: %v", got)
			}
		}
	}
}

// The cap is what turned an ordering mistake into a total one: four slots filled alphabetically
// meant the relevant projects were never reached. With relevance ordering the cap still has to keep
// the front of the list.
func TestCSharpGlobalUsingsFilesFor_capKeepsTheFrontOfTheList(t *testing.T) {
	files := map[string]string{}
	var scope []string
	// Six projects, named so that alphabetical order is the REVERSE of relevance order.
	for _, p := range []string{"a", "b", "c", "d", "e", "f"} {
		files["src/"+p+"/"+p+".csproj"] = guFixtureProj
		files["src/"+p+"/GlobalUsings.cs"] = "global using Ns." + p + ";\n"
		files["src/"+p+"/Thing.cs"] = "namespace " + p + ";\npublic class Thing { }\n"
	}
	root := writeGlobalUsingsRepo(t, files)
	for _, p := range []string{"f", "e", "d", "c", "b", "a"} {
		scope = append(scope, "src/"+p+"/Thing.cs")
	}

	got := csharpGlobalUsingsFilesFor(root, "csharp", scope)
	if len(got) != maxGlobalUsingsFilesInPrompt {
		t.Fatalf("want %d files, got %d: %v", maxGlobalUsingsFilesInPrompt, len(got), got)
	}
	want := []string{"src/f/GlobalUsings.cs", "src/e/GlobalUsings.cs", "src/d/GlobalUsings.cs", "src/c/GlobalUsings.cs"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: want %s, got %s (full: %v)", i, w, got[i], got)
		}
	}
}
