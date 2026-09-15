package evaluator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
)

// keysOfFileMap returns the map's keys in a deterministic order, so a prompt built from them is the
// same on two runs over the same repository.
func keysOfFileMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reCSharpGlobalUsing matches a `global using` declaration, in its plain, static and aliased forms.
var reCSharpGlobalUsing = regexp.MustCompile(`(?m)^\s*global\s+using\s`)

// maxGlobalUsingsFilesInPrompt bounds what this adds to a prompt that is already clamped. Two
// projects — the code under test and the test project — is the shape of every repair round; four
// leaves room for a monorepo round without letting a large solution crowd out the artifacts.
const maxGlobalUsingsFilesInPrompt = 4

// maxProjectRootScan bounds the per-project directory read. Global usings live at the project root
// by convention, and a project root with more files than this is not one this needs to reason about.
const maxProjectRootScan = 64

// csharpGlobalUsingsFilesFor returns the repo-relative files that declare `global using` for the
// projects owning rels.
//
// C# 10 lets a project declare its imports once, in any file, for every file it compiles. The
// consequence for a repair is that the file under repair — and every file the diagnostic names —
// carries no using line for those namespaces, so nothing in the prompt says where its types come
// from. Run api-8d5367b3383017e25f09e53dacf6c275 lost its budget to exactly that:
// CreateContributorHandler takes IRepository<Contributor>, its own file imports only the aggregate
// namespace, and the namespace that resolves the type is named in its project's GlobalUsings.cs.
// evaluator.fix_api_surface_unavailable fired on all eleven fix rounds and the loop stopped on no
// progress at iteration 10 of 20.
//
// The API-surface provider cannot cover this. It reads NuGet XML documentation, and none of that
// repository's packages ship any — ardalis.sharedkernel, ardalis.result, fastendpoints and the rest
// are all assembly-and-pdb only.
//
// Keyed on the DECLARATION rather than the filename: GlobalUsings.cs is a convention, not a rule,
// and a project is free to put them anywhere.
func csharpGlobalUsingsFilesFor(repoRoot, lang string, rels []string) []string {
	root := strings.TrimSpace(repoRoot)
	if root == "" || apisurface.NormalizeLang(lang) != apisurface.LangCSharp {
		return nil
	}
	already := make(map[string]bool, len(rels))
	projects := map[string]bool{}
	for _, rel := range rels {
		clean := filepath.ToSlash(strings.TrimSpace(rel))
		if clean == "" {
			continue
		}
		already[clean] = true
		csproj, ok := dotnetproj.NearestCsprojRel(root, clean)
		if !ok {
			continue
		}
		projects[filepath.ToSlash(filepath.Dir(csproj))] = true
	}

	var out []string
	seen := map[string]bool{}
	dirs := make([]string, 0, len(projects))
	for d := range projects {
		dirs = append(dirs, d)
	}
	// Deterministic: two rounds on the same repository must build the same prompt.
	sort.Strings(dirs)
	for _, dir := range dirs {
		for _, rel := range globalUsingsInProjectRoot(root, dir) {
			if already[rel] || seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
			if len(out) >= maxGlobalUsingsFilesInPrompt {
				return out
			}
		}
	}
	return out
}

// globalUsingsInProjectRoot returns the .cs files directly in a project's root directory that
// declare at least one `global using`. Non-recursive: the declaration is project-wide, and reading
// a whole source tree to find it would cost more than the prompt it feeds.
func globalUsingsInProjectRoot(repoRoot, projectDirRel string) []string {
	absDir := filepath.Join(repoRoot, filepath.FromSlash(projectDirRel))
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}
	var out []string
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".cs") {
			continue
		}
		scanned++
		if scanned > maxProjectRootScan {
			break
		}
		b, rerr := os.ReadFile(filepath.Join(absDir, e.Name()))
		if rerr != nil || !reCSharpGlobalUsing.Match(b) {
			continue
		}
		rel := e.Name()
		if projectDirRel != "" && projectDirRel != "." {
			rel = projectDirRel + "/" + e.Name()
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
