package apisurface

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

var (
	// reCSharpTypeDeclaration matches a type declaration's NAME. Modifiers are skipped rather than
	// enumerated (`public sealed partial record struct Foo`), so the pattern keys on the type
	// keyword itself, which is the one token every form has.
	reCSharpTypeDeclaration = regexp.MustCompile(
		`(?m)\b(?:class|struct|interface|enum|record)\s+(?:class\s+|struct\s+)?([A-Za-z_][A-Za-z0-9_]*)`)

	// reCSharpDelegateDeclaration is separate because a delegate's name does not follow its keyword
	// — the RETURN TYPE does. `public delegate void OrderHandler(Order o)` recorded "void" until
	// this existed, which is both a wrong name and a missing one.
	reCSharpDelegateDeclaration = regexp.MustCompile(
		`(?m)\bdelegate\s+[\w.<>\[\],?\s]+?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^>(]*>)?\s*\(`)
)

// maxCSharpDeclaredWalkDepth bounds the walk. A type twelve directories deep is in a vendored tree,
// not in the code under test.
const maxCSharpDeclaredWalkDepth = 12

// csharpDeclaredSimpleNames walks the repository for the simple type names its own source declares.
//
// Build output is excluded deliberately: obj/ and bin/ hold generated and stale copies, and a name
// found only there would license the opposite of what this function exists to prevent — the prompt
// block that consumes an absence claim tells the model to DELETE the code that uses the name.
//
// Comments and string literals are stripped first, for the same reason: `const string Snippet =
// @"public class InsideAString { }"` declares nothing, and counting it would quietly widen the set
// of names the fixer stops checking.
func csharpDeclaredSimpleNames(repoRoot string) (map[string]bool, bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, false
	}

	out := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // an unreadable subtree makes the set incomplete; the caller must not claim absence
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
		for _, re := range []*regexp.Regexp{reCSharpTypeDeclaration, reCSharpDelegateDeclaration} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				if name := strings.TrimSpace(m[1]); name != "" {
					out[name] = true
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		// An incomplete set is worse than none: every name the walk failed to see would read as
		// proof of absence.
		return nil, false
	}
	return out, true
}
