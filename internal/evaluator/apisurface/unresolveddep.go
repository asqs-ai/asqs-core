package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Generation-time check for references to packages that exist nowhere: not on the project's
// compile classpath, not in the repository.
//
// Run api-a4678e03289277effe4a01043c1bc3ca generated VetControllerE2EIT.java with five call sites
// into org.mozilla.javascript (Rhino) — a dependency spring-petclinic does not have. The system
// prompt forbids new dependencies, but nothing verified the output, so all five reached the
// compiler ("package org.mozilla.javascript does not exist") and then a ten-round fix loop whose
// only lawful repair was rewriting the test bodies — which the fixer never managed.
//
// Provability bounds:
//   - Only explicit dotted references are checked: single-type imports, static imports, and inline
//     qualified usages with at least two lowercase package segments. Bare simple names need scope
//     analysis. Wildcard imports name no class to verify and are skipped.
//   - java.* is assumed present.
//   - A package whose directory exists under the repo's source roots is repo domain, and absence
//     of the named .java file proves nothing (package-private classes live in any file): silent.
//   - The classpath answers through Provider.Lookup on the simple name. No provider, a lookup
//     error, or a candidate list at the resolver's cap (possibly truncated) all mean silence.

var (
	unresolvedImportRE = regexp.MustCompile(`(?m)^\s*import\s+(static\s+)?([\w.]+)\s*;`)
	// unresolvedInlineRE matches `a.b.c.Type` usages: two or more lowercase segments then a
	// capitalised one. The two-segment floor keeps `owner.getPet(...)`-style locals out.
	unresolvedInlineRE = regexp.MustCompile(`(?:^|[^\w.])((?:[a-z_]\w*\.){2,})([A-Z]\w*)`)
)

// resolveSymbolCandidateCap mirrors JavaProvider.resolveSymbol's found-list cap. A result at the
// cap may have stopped scanning early, so membership cannot be disproven from it.
const resolveSymbolCandidateCap = 8

// UnresolvedRef is one reference that provably resolves to nothing at the package it names.
//
// Candidates are the fully-qualified names the compile classpath DOES provide for the same simple
// name. They are the whole reason this is a struct rather than a string: the resolver already
// computes them to decide whether the reference misses, and discarding them turned a one-line
// import repair into "the project has no such dependency" — which points a model at deleting the
// annotation or adding a dependency instead of correcting the package. Run
// api-0c344e6bc0658e0db06506efb9d964f5 lost its whole fix budget to that message: AutoConfigureMockMvc,
// WebMvcTest and MockitoBean all existed on the classpath under Spring Boot 4's relocated packages,
// and the check that proved they were missing from the named package was holding the right answer.
type UnresolvedRef struct {
	Pkg        string
	Cls        string
	Candidates []string
}

// Key identifies the reference itself, independent of how it is worded. Used to diff the refs in a
// fixer's output against the refs already in the file it is repairing.
func (r UnresolvedRef) Key() string { return r.Pkg + "." + r.Cls }

// Reason renders one finding. The branches are different facts and must not be collapsed.
//
// With no candidates nothing on the classpath provides the name at all, which is the only case
// where "the project has no such dependency" is true — and the wording is kept verbatim because it
// IS true there.
//
// With candidates the class exists somewhere else, and how firmly to say so depends on whether the
// candidate is plausibly the same type. A relocation keeps its library prefix
// (org.springframework.boot.test.autoconfigure.web.servlet → org.springframework.boot.webmvc.test.autoconfigure),
// so a shared two-segment prefix earns the directive. A coincidental name collision does not:
// org.mozilla.javascript.Context and io.undertow.Context share a simple name and nothing else, and
// telling a model to "import that instead" there would trade a missing dependency for a wrong type.
// Those still get the fact — the classpath does provide the name — with the judgement left open.
func (r UnresolvedRef) Reason() string {
	if len(r.Candidates) == 0 {
		return "reference to " + r.Key() + ", which is neither on the project compile classpath nor in the repository — the project has no such dependency"
	}
	head := "reference to " + r.Key() + ": no class " + r.Cls + " in package " + r.Pkg +
		" — the compile classpath provides " + r.Cls + " at " + strings.Join(r.Candidates, ", ")
	for _, c := range r.Candidates {
		if sharesPackagePrefix(r.Pkg, c, 2) {
			return head + ". This is the same library under a relocated package: use that import."
		}
	}
	return head + ". If that is the same type, correct the import; if it is an unrelated class that happens to share the name, " + r.Cls + " is unavailable here and the code using it must go."
}

// sharesPackagePrefix reports whether a package and a candidate FQCN agree on their first n
// dot-separated segments.
func sharesPackagePrefix(pkg, candidateFQCN string, n int) bool {
	a := strings.Split(pkg, ".")
	b := strings.Split(candidateFQCN, ".")
	if len(a) < n || len(b) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// UnresolvedDependencyReason reports EVERY reference to a class that provably exists neither on the
// compile classpath nor in the repository, or "" when nothing can be proven.
//
// Reporting all of them, rather than the first in sort order, is what makes the single regeneration
// retry worth its turn. The generated VetControllerE2EIT.java of run
// api-0c344e6bc0658e0db06506efb9d964f5 held four bad package references; ascending (pkg, cls) order
// named org.mockito.MockBean on the first pass and AutoConfigureMockMvc on the retry, and the other
// three reached disk unmentioned and outlived the run.
func UnresolvedDependencyReason(ctx context.Context, provider Provider, repoRoot, testContent string) string {
	refs := UnresolvedDependencyRefs(ctx, provider, repoRoot, testContent)
	if len(refs) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(refs))
	for _, r := range refs {
		reasons = append(reasons, r.Reason())
	}
	return strings.Join(reasons, "; also ")
}

// IntroducedUnresolvedDependencyReason reports references present in after that were not in before.
//
// The fixer gate uses this rather than the absolute check for the reason introducedLowValueFixReason
// exists: a round that repairs three real errors while leaving a fourth untouched must not be
// refused, or the file can never improve. Only a reference the round ADDED is the round's fault.
func IntroducedUnresolvedDependencyReason(ctx context.Context, provider Provider, repoRoot, before, after string) string {
	afterRefs := UnresolvedDependencyRefs(ctx, provider, repoRoot, after)
	if len(afterRefs) == 0 {
		return ""
	}
	had := map[string]bool{}
	for _, r := range UnresolvedDependencyRefs(ctx, provider, repoRoot, before) {
		had[r.Key()] = true
	}
	var reasons []string
	for _, r := range afterRefs {
		if had[r.Key()] {
			continue
		}
		reasons = append(reasons, r.Reason())
	}
	if len(reasons) == 0 {
		return ""
	}
	return strings.Join(reasons, "; also ")
}

// UnresolvedDependencyRefs is the structured form of UnresolvedDependencyReason. Order is the
// deterministic (pkg, cls) order established below, so two calls on the same content agree.
func UnresolvedDependencyRefs(ctx context.Context, provider Provider, repoRoot, testContent string) []UnresolvedRef {
	if provider == nil || strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(testContent) == "" {
		return nil
	}
	stripped := stripJavaStringsAndLineComments(testContent)

	type ref struct{ pkg, cls string }
	seen := map[ref]bool{}
	var refs []ref
	add := func(pkg, cls string) {
		pkg, cls = strings.TrimSpace(pkg), strings.TrimSpace(cls)
		if pkg == "" || cls == "" {
			return
		}
		r := ref{pkg, cls}
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	for _, m := range unresolvedImportRE.FindAllStringSubmatch(stripped, -1) {
		path := m[2]
		if strings.HasSuffix(path, ".*") {
			continue
		}
		if m[1] != "" { // static import: the last segment is a member, the one before it the class.
			if i := strings.LastIndex(path, "."); i > 0 {
				path = path[:i]
			}
		}
		if pkg, cls, ok := splitPackageClass(path); ok {
			add(pkg, cls)
		}
	}
	for _, m := range unresolvedInlineRE.FindAllStringSubmatch(stripped, -1) {
		add(strings.TrimSuffix(m[1], "."), m[2])
	}
	// Deterministic report order regardless of map/regex interleaving.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].pkg != refs[j].pkg {
			return refs[i].pkg < refs[j].pkg
		}
		return refs[i].cls < refs[j].cls
	})

	var out []UnresolvedRef
	for _, r := range refs {
		if strings.HasPrefix(r.pkg+".", "java.") {
			continue
		}
		if repoPackageDirExists(repoRoot, r.pkg) {
			continue // repo domain; file-level absence proves nothing.
		}
		surfaces, err := provider.Lookup(ctx, repoRoot, []Target{{Kind: KindSymbol, Name: r.cls}})
		if err != nil {
			return nil // classpath unavailable: nothing further is provable this round.
		}
		if len(surfaces) >= resolveSymbolCandidateCap {
			continue // possibly truncated scan; membership cannot be disproven.
		}
		found := false
		candidates := make([]string, 0, len(surfaces))
		for _, s := range surfaces {
			if s.FQCN == r.pkg+"."+r.cls {
				found = true
				break
			}
			candidates = append(candidates, s.FQCN)
		}
		if found {
			continue
		}
		sort.Strings(candidates)
		out = append(out, UnresolvedRef{Pkg: r.pkg, Cls: r.cls, Candidates: candidates})
	}
	return out
}

// splitPackageClass splits a dotted path into its lowercase package prefix and the first
// capitalised segment. ok=false when the path has no such shape.
func splitPackageClass(path string) (pkg, cls string, ok bool) {
	segs := strings.Split(path, ".")
	for i, s := range segs {
		if s == "" {
			return "", "", false
		}
		if s[0] >= 'A' && s[0] <= 'Z' {
			if i == 0 {
				return "", "", false
			}
			return strings.Join(segs[:i], "."), s, true
		}
	}
	return "", "", false
}

// repoPackageDirExists reports whether pkg maps to a directory under any repo source root.
func repoPackageDirExists(repoRoot, pkg string) bool {
	rel := strings.ReplaceAll(pkg, ".", "/")
	for _, root := range javaSourceRoots {
		st, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(root+"/"+rel)))
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}
