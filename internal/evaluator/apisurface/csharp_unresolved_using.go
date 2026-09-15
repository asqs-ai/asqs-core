package apisurface

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	return csharpIntroducedUnresolvedUsingReasonWithPackages(before, after, declaredNamespaces, knownNamespaces, nil)
}

// csharpIntroducedUnresolvedUsingReasonWithPackages is csharpIntroducedUnresolvedUsingReason with the
// referenced-package ids as a third evidence set, matched case-insensitively.
//
// Package ids are kept apart from the two namespace sets rather than merged into them because they
// are evidence of a different kind: a namespace a source file imports is observed, a namespace a
// package id implies is inferred from convention. Keeping them separate is what lets the case rule
// apply to the inferred set alone.
func csharpIntroducedUnresolvedUsingReasonWithPackages(before, after string, declaredNamespaces, knownNamespaces, packageIDs map[string]bool) string {
	if len(declaredNamespaces) == 0 && len(knownNamespaces) == 0 && len(packageIDs) == 0 {
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
		if csharpNamespaceCoveredByPackage(ns, packageIDs) {
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

// csharpNamespaceCoveredByPackage reports whether a referenced package id accounts for ns.
//
// Both directions count, and for different reasons. A package is an ANCESTOR of the namespace when
// it ships more than one: `xunit` provides Xunit.Abstractions. A package is a DESCENDANT when the
// repository references a satellite of a larger namespace: Microsoft.EntityFrameworkCore.InMemory
// establishes that Microsoft.EntityFrameworkCore resolves. Requiring exact equality would refuse
// both, which is the false-refusal failure this evidence set exists to end.
//
// Case-insensitive throughout: NuGet ids follow the namespace by convention but not in case, and
// the set is stored lowercased by csharpRepoNamespaceEvidence.
func csharpNamespaceCoveredByPackage(ns string, packageIDs map[string]bool) bool {
	if len(packageIDs) == 0 {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(ns))
	if lower == "" {
		return false
	}
	if packageIDs[lower] {
		return true
	}
	prefix := lower + "."
	for id := range packageIDs {
		if strings.HasPrefix(id, prefix) || strings.HasPrefix(lower, id+".") {
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
// The evidence is the REPOSITORY — every namespace its sources declare, every namespace those
// sources already import, and every NuGet package its projects reference — not the handful of files
// the fix prompt happened to carry.
//
// That distinction is the whole point of this function. The prompt is clamped to a rune budget, and
// the clamp bites hardest on the late, difficult rounds where the fixer most needs a repair to land.
// Reading evidence from it made the gate's confidence inversely proportional to its information:
// the more context was dropped, the more namespaces looked invented. Run
// api-bdf7539b296a0df65a7cf1bf2bf2739b is the cost. The fixer proposed `using Ardalis.Result;` for a
// handler returning Result<int>; seven files in that repository import that namespace and the
// clamped prompt carried none of them, so the gate called it invented and refused the repair on
// three consecutive rounds. The third refusal tripped the consecutive-unusable breaker and ended the
// run at iteration 4 of a 20-iteration budget. In the same run the gate correctly refused
// `using Mediator;` — genuinely absent — so the rule was never wrong about the repository, only
// about which repository it was looking at.
//
// repoFiles is still folded in, because a file the fixer has written this round may be newer than
// the tree walk; it can only ever WIDEN the evidence. It is never a substitute for it: without a
// readable repository the gate says nothing at all, since a negative claim with a partial view
// behind it is exactly the failure above.
func CSharpIntroducedUnresolvedUsingReason(before, after, repoRoot string, repoFiles map[string]string) string {
	declared, known, packages, ok := csharpRepoNamespaceEvidence(repoRoot)
	if !ok {
		return ""
	}
	promptDeclared, promptKnown := csharpNamespaceEvidence(repoFiles)
	for ns := range promptDeclared {
		declared[ns] = true
	}
	for ns := range promptKnown {
		known[ns] = true
	}
	return csharpIntroducedUnresolvedUsingReasonWithPackages(before, after, declared, known, packages)
}

// csharpRepoNamespaceEvidence walks the repository for what its own code proves is reachable:
// the namespaces it declares, the namespaces it already imports, and the ids of the packages its
// projects reference.
//
// ok is false when the tree cannot be read in full. An incomplete walk is worse than no walk,
// because every namespace the walk failed to see reads as proof of absence — the same reasoning
// csharpDeclaredSimpleNames applies one file over.
func csharpRepoNamespaceEvidence(repoRoot string) (declared, known, packages map[string]bool, ok bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	if root == "" || root == "." {
		return nil, nil, nil, false
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, nil, nil, false
	}

	declared, known, packages = map[string]bool{}, map[string]bool{}, map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCSharpNamespaceWalkDepth {
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
		body := string(b)
		for _, m := range reCSharpNamespaceDeclaration.FindAllStringSubmatch(
			dotnetproj.StripCSharpCommentsAndStrings(body), -1) {
			if ns := strings.TrimSpace(m[1]); ns != "" {
				declared[ns] = true
			}
		}
		for ns := range csharpUsingNamespaces(body) {
			known[ns] = true
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, nil, false
	}
	// A package id is the namespace root its assemblies ship under, by NuGet convention strong
	// enough to rely on for a claim that only ever WIDENS what is allowed. Matched case-insensitively
	// because the convention stops short of case: the `xunit` package ships `Xunit`, and a
	// case-sensitive miss would reinstate the false refusal for the commonest test package there is.
	for _, id := range csharpRepoPackageIDs(root) {
		if id = strings.TrimSpace(id); id != "" {
			packages[strings.ToLower(id)] = true
		}
	}
	return declared, known, packages, true
}

// maxCSharpNamespaceWalkDepth bounds the walk, matching maxCSharpDeclaredWalkDepth: a namespace
// twelve directories deep is in a vendored tree, not in the code under test.
const maxCSharpNamespaceWalkDepth = 12

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
