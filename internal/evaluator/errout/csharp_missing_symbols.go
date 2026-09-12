package errout

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/evaluator/errloc"
)

var (
	// CS0246: The type or namespace name 'X' could not be found …
	// CS0234: The type or namespace name 'X' does not exist in the namespace 'N' …
	reCSharpMissingTypeName = regexp.MustCompile(
		`type or namespace name '([A-Za-z_][A-Za-z0-9_]*)(?:<[^']*>)?'`)
	// CS0103: The name 'X' does not exist in the current context
	reCSharpMissingName = regexp.MustCompile(`The name '([A-Za-z_][A-Za-z0-9_]*)' does not exist`)
)

// csharpMissingTypeFiles resolves the type names a C# diagnostic could not find to the repository
// files that declare them.
//
// The repair for CS0246 and CS0234 is almost always a `using`, and the namespace is stated in the
// declaring file's own source — so putting that file in the prompt turns a guess into a reading.
// ResolveMissingTypeFiles answered for Java and Kotlin only, so a C# round had to guess.
//
// A type the repository does not declare yields nothing: the prompt's file budget is finite, and an
// irrelevant file displaces a relevant one.
func csharpMissingTypeFiles(raw, repoRoot string, limit int) []string {
	wanted := map[string]bool{}
	for _, re := range []*regexp.Regexp{reCSharpMissingTypeName, reCSharpMissingName} {
		for _, m := range re.FindAllStringSubmatch(raw, -1) {
			if n := strings.TrimSpace(m[1]); n != "" {
				wanted[n] = true
			}
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	root := filepath.Clean(repoRoot)
	// Deterministic across runs: the walk visits in directory order, but two repositories with the
	// same content must produce the same prompt.
	byName := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCSharpMissingTypeWalkDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".cs") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(string(b))
		for _, m := range reCSharpDeclaredTypeName.FindAllStringSubmatch(src, -1) {
			name := strings.TrimSpace(m[1])
			if !wanted[name] {
				continue
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				continue
			}
			rel = errloc.NormalizePath(rel)
			// First declaration wins, and ties break on the shorter path: a type declared in both
			// a source tree and a sample directory should offer the one nearer the root.
			if prev, ok := byName[name]; !ok || len(rel) < len(prev) {
				byName[name] = rel
			}
		}
		return nil
	})
	if len(byName) == 0 {
		return nil
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		rel := byName[n]
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// reCSharpDeclaredTypeName matches a type declaration's name. Delegates are excluded: their keyword
// is followed by a return type, and a delegate is never the subject of a missing-type diagnostic a
// source file can answer.
var reCSharpDeclaredTypeName = regexp.MustCompile(
	`(?m)\b(?:class|struct|interface|enum|record)\s+(?:class\s+|struct\s+)?([A-Za-z_][A-Za-z0-9_]*)`)

const maxCSharpMissingTypeWalkDepth = 12
