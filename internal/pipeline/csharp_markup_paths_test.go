package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// The Roslyn indexer reads .cs and nothing else, so IndexablePaths built from the parsed map could
// never contain a Razor page or a Blazor component — they were filtered out before the index phase
// saw them. A C# UI repository therefore indexed its code-behind with no page: no `@page` route, no
// rendered text, and nothing for a selector inventory to be built from.
func TestAppendCSharpMarkupPaths(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{
		"src/Shop/Pages/Orders/Index.cshtml",
		"src/Shop/Views/Home/Index.cshtml",
		"src/Shop/Components/Counter.razor",
		"src/Shop/wwwroot/index.html",
		"src/Shop/Program.cs",
		// Build output must not be indexed: obj/ holds generated copies of the same markup.
		"src/Shop/obj/Debug/net8.0/Razor/Pages/Orders/Index.cshtml",
		"src/Shop/bin/Release/net8.0/wwwroot/index.html",
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := map[string]struct{}{"src/Shop/Program.cs": {}}
	got := appendCSharpMarkupPaths(in, repo)

	for _, want := range []string{
		"src/Shop/Pages/Orders/Index.cshtml",
		"src/Shop/Views/Home/Index.cshtml",
		"src/Shop/Components/Counter.razor",
		"src/Shop/wwwroot/index.html",
		"src/Shop/Program.cs", // the caller's entries survive
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s; got %v", want, got)
		}
	}
	for _, unwanted := range []string{
		"src/Shop/obj/Debug/net8.0/Razor/Pages/Orders/Index.cshtml",
		"src/Shop/bin/Release/net8.0/wwwroot/index.html",
	} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("indexed build output: %s", unwanted)
		}
	}
}

// A nil set is what a caller with nothing to add passes, and a missing repository is not an error
// worth failing an indexing run over.
func TestAppendCSharpMarkupPaths_degradesQuietly(t *testing.T) {
	if got := appendCSharpMarkupPaths(nil, ""); len(got) != 0 {
		t.Errorf("appendCSharpMarkupPaths(nil, \"\") = %v, want empty", got)
	}
	if got := appendCSharpMarkupPaths(nil, filepath.Join(t.TempDir(), "gone")); len(got) != 0 {
		t.Errorf("a missing repository produced %v", got)
	}
}
