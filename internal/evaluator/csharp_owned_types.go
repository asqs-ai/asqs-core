package evaluator

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/langid"
)

// repoDeclaredTypeNamesForLang derives the type names the repository declares, from the files
// already loaded into the fix prompt, keyed by language.
//
// It feeds FilterOwnedTypes, which stops the API-surface lookup spending slots on types the
// repository declares itself: the classpath cannot answer for those, and on the fixer's path they
// are exactly the ones not yet compiled. Java derived them from the src/main/java path mirror,
// which is exact. C# has no such mirror — its namespace is declared in the file, not implied by the
// path — so it is read from the source, and the namespace is taken from the file's own declaration.
//
// Both the fully-qualified and the simple name are recorded for C#: a CS1061 names the type without
// its namespace, and a bare-name target would otherwise still consume a lookup slot.
func repoDeclaredTypeNamesForLang(lang string, files map[string]string) map[string]bool {
	switch {
	case langid.IsJava(lang):
		return repoDeclaredTypeNames(files)
	case langid.IsCSharp(lang):
		return csharpRepoDeclaredTypeNames(files)
	default:
		return nil
	}
}

var (
	// reCSharpNamespace matches both forms: `namespace Shop.Core;` (file-scoped) and
	// `namespace Shop.Core {` (block-scoped).
	reCSharpNamespace = regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_.]*)`)
	// reCSharpDeclaredType matches a type declaration's name; delegates are excluded because their
	// keyword is followed by a return type rather than the name, and a delegate is never the
	// subject of a member lookup.
	reCSharpDeclaredType = regexp.MustCompile(
		`(?m)\b(?:class|struct|interface|enum|record)\s+(?:class\s+|struct\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
)

func csharpRepoDeclaredTypeNames(files map[string]string) map[string]bool {
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]bool, len(files))
	for path, body := range files {
		if !strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".cs") {
			continue
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(body)
		ns := ""
		if m := reCSharpNamespace.FindStringSubmatch(src); len(m) > 1 {
			ns = strings.TrimSpace(m[1])
		}
		for _, m := range reCSharpDeclaredType.FindAllStringSubmatch(src, -1) {
			name := strings.TrimSpace(m[1])
			if name == "" {
				continue
			}
			out[name] = true
			if ns != "" {
				out[ns+"."+name] = true
			}
		}
	}
	return out
}
