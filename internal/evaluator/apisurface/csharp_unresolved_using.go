package apisurface

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// reCSharpUsingDirective matches every form of a using DIRECTIVE — plain, aliased, static and
// global — and captures the namespace being brought into scope.
//
// A `using (var x = ...)` STATEMENT is excluded by requiring the terminating semicolon with no
// opening parenthesis: the statement form is the one place this pattern could produce a namespace
// that does not exist and blame the round for it.
var reCSharpUsingDirective = regexp.MustCompile(
	`(?m)^\s*(?:global\s+)?using\s+(?:static\s+)?(?:[A-Za-z_][A-Za-z0-9_]*\s*=\s*)?([A-Za-z_][A-Za-z0-9_.]*)\s*;`)

// csharpIntroducedUnresolvedUsingReason refuses a repair round that ADDED a `using` for a namespace
// that exists neither in the repository nor in the packages it references.
//
// Such a round trades one compile error for another, and the loop spends a whole round discovering
// that. The Java gate has refused this since the classpath work; the C# arm returned "" for every
// input, so a round that invented `using Shop.Core.Repositories;` was accepted and written to disk.
//
// Three bounds make the negative claim safe to act on:
//
//   - Only usings the round ADDED are judged. One already present is the state the round inherited,
//     and refusing it would block every repair on a file that already fails to compile.
//   - With no evidence — neither a declared-namespace set nor a package closure — the gate stays
//     silent. A negative claim with nothing behind it rejects correct repairs, which is worse than
//     the problem it solves.
//   - A namespace that is a PREFIX of a known one counts as known: `using System;` is satisfied by
//     a closure that knows `System.Net.Http`, and the repo's own `Shop` by `Shop.Core.Models`.
//
// declaredNamespaces are the namespaces the repository's own sources declare. knownNamespaces are
// those the referenced packages provide. Either may be nil.
func csharpIntroducedUnresolvedUsingReason(before, after string, declaredNamespaces, knownNamespaces map[string]bool) string {
	if len(declaredNamespaces) == 0 && len(knownNamespaces) == 0 {
		return ""
	}
	had := csharpUsingNamespaces(before)
	var unresolved []string
	for ns := range csharpUsingNamespaces(after) {
		if had[ns] {
			continue // inherited, not introduced
		}
		if csharpNamespaceKnown(ns, declaredNamespaces) || csharpNamespaceKnown(ns, knownNamespaces) {
			continue
		}
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
		"this round added a using for %s %s, which exists in neither this repository's own sources nor the packages it references — "+
			"the file would not compile. Use a namespace the repository or its packages actually provide.",
		plural, strings.Join(quoteAll(unresolved), ", "))
}

// csharpUsingNamespaces returns the set of namespaces a file brings into scope. Comments and string
// literals are stripped first: a using inside a template string is not a directive.
func csharpUsingNamespaces(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reCSharpUsingDirective.FindAllStringSubmatch(
		dotnetproj.StripCSharpCommentsAndStrings(src), -1) {
		if ns := strings.TrimSpace(m[1]); ns != "" {
			out[ns] = true
		}
	}
	return out
}

// csharpNamespaceKnown reports whether ns is, or is an ancestor of, a namespace in the set. The
// ancestor rule matters: a closure derived from type names knows `System.Net.Http` and must not
// therefore call `using System;` unresolvable.
func csharpNamespaceKnown(ns string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	if set[ns] {
		return true
	}
	prefix := ns + "."
	for known := range set {
		if strings.HasPrefix(known, prefix) {
			return true
		}
	}
	return false
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, `"`+s+`"`)
	}
	return out
}

// CSharpIntroducedUnresolvedUsingReason is the exported entry point for the fixer.
//
// The evidence comes from the repository itself — the namespaces its sources declare and the
// namespaces the types in its own files reference — rather than from a package closure this package
// cannot resolve without a restore. That is a weaker set than Java's classpath, which is why the
// ancestor rule is generous and why an empty set means silence.
func CSharpIntroducedUnresolvedUsingReason(before, after string, repoFiles map[string]string) string {
	declared, known := csharpNamespaceEvidence(repoFiles)
	return csharpIntroducedUnresolvedUsingReason(before, after, declared, known)
}

// csharpNamespaceEvidence derives what namespaces are reachable, from the files already loaded into
// the fix prompt: those the repo DECLARES, and those its own files already `using`.
//
// The second half is what makes the gate usable without a restore. If any file in this repository
// compiles today with `using Microsoft.EntityFrameworkCore;`, then that namespace resolves here, and
// a round adding it is not inventing anything.
func csharpNamespaceEvidence(repoFiles map[string]string) (declared, known map[string]bool) {
	declared = map[string]bool{}
	known = map[string]bool{}
	for path, body := range repoFiles {
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".cs") {
			continue
		}
		src := dotnetproj.StripCSharpCommentsAndStrings(body)
		for _, m := range reCSharpNamespaceDeclaration.FindAllStringSubmatch(src, -1) {
			if ns := strings.TrimSpace(m[1]); ns != "" {
				declared[ns] = true
			}
		}
		for ns := range csharpUsingNamespaces(body) {
			known[ns] = true
		}
	}
	return declared, known
}

// reCSharpNamespaceDeclaration matches both the file-scoped and block-scoped forms.
var reCSharpNamespaceDeclaration = regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_.]*)`)
