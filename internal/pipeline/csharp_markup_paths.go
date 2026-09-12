package pipeline

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// csharpMarkupExtensions are the files a C#-primary run has to index besides its sources.
//
// A Razor page, a Razor view and a Blazor component are where a web application's UI actually
// lives: its routes (`@page "/orders"`), the text a browser test asserts on, and the test hooks a
// selector inventory is built from. The Roslyn indexer reads `.cs` and nothing else, so
// IndexablePaths derived from the parsed map could never contain one of these — they were filtered
// out before the index phase saw them, and a C# UI repository indexed its code-behind with no page.
var csharpMarkupExtensions = map[string]bool{
	".cshtml": true,
	".razor":  true,
	".html":   true,
}

// maxCSharpMarkupWalkDepth bounds the walk, matching the other .NET walks in this codebase.
const maxCSharpMarkupWalkDepth = 12

// appendCSharpMarkupPaths adds the repository's markup files to an indexable-path set.
func appendCSharpMarkupPaths(paths map[string]struct{}, repoRoot string) map[string]struct{} {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	if root == "" {
		return paths
	}
	if paths == nil {
		paths = map[string]struct{}{}
	}
	var added []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCSharpMarkupWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !csharpMarkupExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, dup := paths[rel]; dup {
			return nil
		}
		added = append(added, rel)
		return nil
	})
	for _, rel := range added {
		paths[rel] = struct{}{}
	}
	return paths
}
