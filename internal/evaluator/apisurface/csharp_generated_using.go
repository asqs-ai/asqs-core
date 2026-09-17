package apisurface

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// CSharpUnresolvedRepoUsingReason refuses a generated C# test that imports a namespace under this
// repository's own root that the repository does not declare.
//
// The fix loop has a broader version of this gate, but it can afford one: it judges only the usings
// a ROUND ADDED, against the state that round inherited. Generation has no baseline — every using
// in a new file is new — so the same rule here would reject `using Xunit;` in the first test a
// repository ever receives, which is the exact situation this pipeline exists to create.
//
// What survives the loss of the baseline is the case actually on record. When the root namespace
// belongs to the repository, no package can supply the rest of it: `using Shop.Core.Repositories;`
// in a repository whose namespaces are Shop.Core and Shop.Api names something that exists nowhere,
// and the round that wrote it has traded one compile error for another. A third-party root is never
// judged, because nothing in the repository's sources bears on what a package provides.
func CSharpUnresolvedRepoUsingReason(repoRoot, content string) string {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(content) == "" {
		return ""
	}
	declared, ok := csharpRepoNamespaces(repoRoot)
	if !ok || len(declared) == 0 {
		return ""
	}
	roots := map[string]bool{}
	for ns := range declared {
		roots[csharpNamespaceRoot(ns)] = true
	}

	var unresolved []string
	seen := map[string]bool{}
	for ns := range csharpUsingNamespaces(content) {
		if !roots[csharpNamespaceRoot(ns)] || seen[ns] {
			continue // not this repository's to answer for
		}
		if csharpNamespaceKnown(ns, declared) {
			continue
		}
		seen[ns] = true
		unresolved = append(unresolved, ns)
	}
	if len(unresolved) == 0 {
		return ""
	}
	sort.Strings(unresolved)
	plural := "namespace"
	if len(unresolved) > 1 {
		plural = "namespaces"
	}
	return fmt.Sprintf(
		"using directive for %s %s, which is under this repository's own namespace root and which "+
			"the repository does not declare anywhere — no package can supply it. Use a namespace the "+
			"sources in this prompt actually declare.",
		plural, strings.Join(quoteAll(unresolved), ", "))
}

func csharpNamespaceRoot(ns string) string {
	if i := strings.IndexByte(ns, '.'); i > 0 {
		return ns[:i]
	}
	return ns
}

// csharpRepoNamespaces walks the repository for the namespaces its own sources declare. It reports
// false for an unreadable subtree: an incomplete set would call a real namespace invented.
func csharpRepoNamespaces(repoRoot string) (map[string]bool, bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, false
	}
	out := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCSharpDeclaredWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".cs") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(string(b))
		for _, m := range reCSharpNamespaceDeclaration.FindAllStringSubmatch(src, -1) {
			if ns := strings.TrimSpace(m[1]); ns != "" {
				out[ns] = true
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, false
	}
	return out, true
}
