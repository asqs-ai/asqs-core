package apisurface

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// maxCSharpProjectScan bounds how many project files contribute to the closure. A solution with
// more than this many projects has a package set far wider than one prompt can use anyway.
const maxCSharpProjectScan = 64

// csharpRepoPackageIDs returns every NuGet package id the repository's projects reference.
//
// This is the search bound for a bare type name. A bare name carries no namespace — that is what
// makes it bare — so there is nothing to narrow the package cache by, and the cache holds every
// version of every package this machine has ever restored. The repository's own closure is the
// narrowing that is both correct and cheap: a type it can actually import comes from a package it
// actually references.
//
// Resolution goes through dotnetproj, so a version held in Directory.Packages.props under central
// package management is found the same way the rest of the pipeline finds it.
func csharpRepoPackageIDs(repoPath string) []string {
	root := filepath.Clean(strings.TrimSpace(repoPath))
	if root == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	projects := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && dotnetproj.WalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		projects++
		if projects > maxCSharpProjectScan {
			return fs.SkipAll
		}
		facts, ferr := dotnetproj.ResolveFacts(root, path)
		if ferr != nil {
			return nil
		}
		for _, id := range facts.PackageIDs() {
			if id = strings.TrimSpace(id); id != "" && !seen[strings.ToLower(id)] {
				seen[strings.ToLower(id)] = true
				out = append(out, id)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}
