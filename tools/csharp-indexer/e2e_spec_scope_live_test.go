package csharpindexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFunctionalTestProjectFixture is the shape that broke run api-8d5367b3383017e25f09e53dacf6c275:
// a functional-test project holding one real spec beside two files that are not specs at all.
func writeFunctionalTestProjectFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>`,
		"src/App/Widget.cs": `namespace Fixture.App;
public sealed class Widget
{
    public string Name() => "w";
}
`,
		"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings></PropertyGroup><ItemGroup><ProjectReference Include="..\..\src\App\App.csproj" /></ItemGroup></Project>`,

		// A real spec: has a test method.
		"tests/App.Tests/WidgetSpec.cs": `using Microsoft.AspNetCore.Mvc.Testing;
using Xunit;

namespace Fixture.Tests;

public class WidgetSpec
{
    [Fact]
    public void ReturnsName()
    {
        var w = new Fixture.App.Widget();
        Assert.Equal("w", w.Name());
    }
}
`,
		// The shared harness. Mentions WebApplicationFactory, declares no test method. Every other
		// test in the project depends on it, so generating "a test" into it breaks the whole suite.
		"tests/App.Tests/CustomWebApplicationFactory.cs": `using Microsoft.AspNetCore.Mvc.Testing;

namespace Fixture.Tests;

public class CustomWebApplicationFactory<TProgram> : WebApplicationFactory<TProgram> where TProgram : class
{
    public string Marker() => "harness";
}
`,
		// Global usings. Declares no namespace and no type; carries the project's import graph.
		"tests/App.Tests/GlobalUsings.cs": `global using Microsoft.AspNetCore.Mvc.Testing;
global using Xunit;
`,
	}
	for rel, content := range files {
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// E2E_SPEC marks a file as "an end-to-end test to extend". The emission condition was
// `(isTest || ContainsTestAttribute(text)) && e2eFramework != null`, and isTest is true for EVERY
// file in a test project while DetectE2EFramework matches the bare substring "WebApplicationFactory"
// — so the shared harness and a global-usings file both qualified.
//
// Run api-8d5367b3383017e25f09e53dacf6c275 is what that costs. Four of its fourteen gaps were
// E2E_SPEC anchors in the root functional-test project; two of the four were these files. ASQS wrote
// to CustomWebApplicationFactory.cs, and the run ended with every functional test failing:
// "Cannot create an instance of CustomWebApplicationFactory`1[TProgram] because
// Type.ContainsGenericParameters is true."
//
// A spec is a file with a test method. Nothing else is one.
func TestIndexer_e2eSpecRequiresATestMethod(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), writeFunctionalTestProjectFixture(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	hasE2ESpec := func(rel string) bool {
		pf := m[rel]
		if pf == nil {
			t.Fatalf("%s not indexed; got %v", rel, keysOf(m))
		}
		for _, s := range pf.Symbols {
			if s.Kind == "E2E_SPEC" {
				return true
			}
		}
		return false
	}

	if !hasE2ESpec("tests/App.Tests/WidgetSpec.cs") {
		t.Error("a file with a test method is a spec and must keep its E2E_SPEC anchor")
	}
	if hasE2ESpec("tests/App.Tests/CustomWebApplicationFactory.cs") {
		t.Error("the shared harness has no test method; anchoring a gap to it invites a rewrite that breaks every test in the project")
	}
	if hasE2ESpec("tests/App.Tests/GlobalUsings.cs") {
		t.Error("a global-usings file declares no test; it is not a spec")
	}
}

// TEST_SELECTOR symbols are linked to "E2E_SPEC:"+relPath, and CollectTestSelectors is gated on the
// same isTest the E2E_SPEC emission used. Narrowing one without the other would leave selector
// edges pointing at a symbol that no longer exists.
func TestIndexer_testSelectorsDoNotOutliveTheirSpec(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), writeFunctionalTestProjectFixture(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, rel := range []string{
		"tests/App.Tests/CustomWebApplicationFactory.cs",
		"tests/App.Tests/GlobalUsings.cs",
	} {
		pf := m[rel]
		if pf == nil {
			continue
		}
		for _, e := range pf.Edges {
			if e.CallerFQName == "E2E_SPEC:"+rel {
				t.Errorf("%s: edge %s -> %s names an E2E_SPEC symbol this file no longer declares",
					rel, e.CallerFQName, e.CalleeFQName)
			}
		}
		for _, s := range pf.Symbols {
			if s.Kind == "TEST_SELECTOR" {
				t.Errorf("%s: TEST_SELECTOR symbol survives without its spec", rel)
			}
		}
	}
}

// A GlobalUsings.cs declares no namespace, so moduleNs is empty and the using-directive loop's
// `if (string.IsNullOrEmpty(moduleNs)) continue;` dropped every import in it: no symbol, no chunk,
// no IMPORTS edge. In the fixture repository 14 of 16 such files indexed to nothing at all.
//
// That is the file that answers "which namespace provides this type" for every other file in the
// project. Without it the fixer guessed: api-8d5367b3383017e25f09e53dacf6c275 spent eight of its ten
// iterations failing to resolve IRepository<> before the loop gave up on no progress.
func TestIndexer_globalUsingsContributeImports(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), writeFunctionalTestProjectFixture(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	const rel = "tests/App.Tests/GlobalUsings.cs"
	pf := m[rel]
	if pf == nil {
		t.Fatalf("%s not indexed; got %v", rel, keysOf(m))
	}
	if len(pf.Symbols) == 0 {
		t.Fatalf("%s produced no symbol at all, so nothing can retrieve its imports", rel)
	}
	want := map[string]bool{
		"Microsoft.AspNetCore.Mvc.Testing": false,
		"Xunit":                            false,
	}
	for _, e := range pf.Edges {
		if _, ok := want[e.CalleeFQName]; ok {
			want[e.CalleeFQName] = true
		}
	}
	for ns, got := range want {
		if !got {
			t.Errorf("no IMPORTS edge for global using %q; edges = %+v", ns, pf.Edges)
		}
	}
}
