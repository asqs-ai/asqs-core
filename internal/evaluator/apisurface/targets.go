// Package apisurface extracts real API signatures from a project's compile classpath so the fixer
// can repair wrong-API errors instead of guessing at them again.
//
// The motivating evidence: in run api-d7e0cbece3e9260f73836f5d50d21c96 the fixer repaired every
// error whose fix was inferable from the symbol name plus the manifest (missing imports — 4 files,
// all fixed) and repaired none of the errors that required a third-party method signature
// (`hasURLContaining` on Playwright's PageAssertions, `hasStatus`/`hasHeader` on
// APIResponseAssertions, `assertThat(() -> …)` instead of AssertJ's assertThatThrownBy — 3 files,
// zero deletions across four rounds). The partition was exact.
//
// The answer was not in the repository. A search of the entire workspace found the only occurrences
// of `PageAssertions`/`hasURL*` to be the two broken lines themselves, and no occurrence of
// `assertThatThrownBy` at all — so no retrieval strategy over the repo could have supplied it.
// It was, however, sitting in the jars Maven had already downloaded:
//
//	javap -cp playwright-1.49.0.jar com.microsoft.playwright.assertions.PageAssertions
//	  public default void hasURL(java.lang.String);          <- no hasURLContaining
//	jar tf spring-core.jar | grep RuntimeHints
//	  org/springframework/aot/hint/RuntimeHints.class         <- the missing import
//
// The extraction targets are derivable from the javac diagnostic itself, which is what this file
// does. Nothing here talks to a network or a model: it is a deterministic read of what the compiler
// already said and what is already on disk.
package apisurface

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TargetKind distinguishes what we know about a target.
type TargetKind string

const (
	// KindType is a fully-qualified type whose member list we need — the diagnostic named the type
	// but rejected a member ("cannot find symbol: method X, location: interface a.b.C").
	KindType TargetKind = "type"
	// KindSymbol is a bare simple name that did not resolve at all, so we need to find which
	// classpath type provides it ("cannot find symbol: class RuntimeHints").
	KindSymbol TargetKind = "symbol"
)

// Target is one thing to look up on the classpath.
type Target struct {
	Kind TargetKind
	// Name is a fully-qualified name for KindType, a simple name for KindSymbol.
	Name string
	// Member is the specific member the diagnostic rejected, when it named one. Used to rank the
	// member list so a near-miss (hasURLContaining -> hasURL) surfaces first.
	Member string
}

// javac's `cannot find symbol` is a two-line record with a closed vocabulary, not a family of
// unrelated messages:
//
//	error: cannot find symbol
//	  symbol:   <kind> <name>[(args)]
//	  location: <kind> <qualified>[ of type <T>]
//
// `symbol` kinds in practice are class, method, variable, constructor and package; `location` adds
// interface, enum, record, annotation, module and the `variable v of type T` form. That is roughly
// forty (symbol, location) pairs, and this file used to carry one hand-written regex per pair —
// which is why every new repository appeared to reveal a "new" pattern. They were never new; they
// were uncovered cells of the same grid, and a stalled run was the only way to discover one.
//
// Two of the cells found that way are not exotic at all: a wrong enum constant
// (`symbol: variable BLUE / location: class Colour`) and any unimported type used as a static
// access (`symbol: variable Widget / location: class MyTest`). Both yielded no target at all.
//
// So the block is parsed structurally — read both lines, then decide from (symbolKind,
// locationKind) — and every pair is covered, including the ones nobody has hit yet. The patterns
// below are only for javac's OTHER diagnostics, which really are distinct messages with their own
// text; that list is closed and enumerable from javac's compiler.properties.
var (
	// javacSymbolLine captures the `symbol:` detail line of a cannot-find-symbol record.
	javacSymbolLine = regexp.MustCompile(`symbol:\s+([^\n]+)`)
	// javacLocationAfterSymbol matches the `location:` line ONLY when it immediately follows the
	// symbol line, so a location belonging to a later diagnostic can never be paired with this
	// one. Anchored, and applied to the remainder after a javacSymbolLine match.
	javacLocationAfterSymbol = regexp.MustCompile(`^\n\s*location:\s+([^\n]+)`)
	// javacMissingPackage matches "package X does not exist", which javac emits for a qualified use
	// of a type that was never imported (`RuntimeHints.Resources` when RuntimeHints is unknown).
	javacMissingPackage = regexp.MustCompile(`package\s+([\w.$]+)\s+does not exist`)
	// javacAmbiguous matches "reference to assertThat is ambiguous ... in a.b.C and ... in a.b.C".
	// The type is named twice and is the surface worth showing: the real fix is almost always a
	// different entry point on the same class (assertThat -> assertThatThrownBy).
	javacAmbiguous = regexp.MustCompile(`reference to (\w+) is ambiguous[^\n]*\n[^\n]*?\sin\s+([\w.$]+)\s+and\b`)
	// javacNotApplicable matches the "method a.b.C.m(T) is not applicable" detail lines, which name
	// the type whose overloads the model got wrong.
	javacNotApplicable = regexp.MustCompile(`method\s+([\w.$]+)\.(\w+)\([^)]*\)\s+is not applicable`)
	// javacNoSuitableMethod matches the HEADER of the same diagnostic:
	//
	//	no suitable method found for assertThat(int)
	//	    method PlaywrightAssertions.assertThat(Page) is not applicable
	//	    method PlaywrightAssertions.assertThat(Locator) is not applicable
	//
	// The distinction from javacNotApplicable is the whole point. Those detail lines name the types
	// javac ALREADY RULED OUT, and dumping their members answers a question the compiler has
	// answered: no, not on this class. When the model reached for the wrong class entirely — an
	// AssertJ value assertion written against Playwright's static assertThat — the surface block
	// then argues it INTO the dead end, because it is rendered under "these are the ONLY members
	// that exist" with a complete-list footer.
	//
	// Run api-5e5535208f4ba61613f60c345ba9b567 spent its last four rounds there: every round
	// resolved PlaywrightAssertions (4 members, truncated=false) against `assertThat(int)`, every
	// round the model edited around line 63, and the primary-site guard reverted it. The type that
	// answers the error, org.assertj.core.api.Assertions, was never looked up.
	javacNoSuitableMethod = regexp.MustCompile(`no suitable method found for (\w+)\(`)
	// javacConstructorArity matches "constructor P in class a.b.C cannot be applied to given types".
	javacConstructorArity = regexp.MustCompile(`constructor\s+(\w+)\s+in\s+(?:class|interface|enum|record)\s+([\w.$]+)\s+cannot be applied`)
	// javacPrivateAccess matches "X(args) has private access in a.b.C" and the member form
	// "x has private access in a.b.C".
	javacPrivateAccess = regexp.MustCompile(`(\w+)(?:\([^)]*\))?\s+has\s+(?:private|protected)\s+access\s+in\s+([\w.$]+)`)
)

// Roslyn and tsc. Until these existed this file was entirely javac, and ParseTargets is not
// language-gated — so a C# or TypeScript run resolved ZERO targets from its diagnostics on every
// round, and the fixer's API-surface block could only ever carry the generation-time framework list
// from PregenerateTargets. The providers were built and wired and simply never asked a question.
//
// Both compilers quote every name they report, which is what makes these safe to run unconditionally
// alongside the javac patterns: no javac diagnostic has this shape, and none of the javac patterns
// match these. Mixed output (a polyglot repo, a monorepo build) is parsed correctly for the same
// reason.
var (
	// CS1061 (`'Widget' does not contain a definition for 'Rename' and no accessible extension
	// method …`) and CS0117 (`'Colour' does not contain a definition for 'Blue'`). One pattern
	// because the leading clause is identical and it is the whole clause we need: the type and the
	// member it rejected, which is exactly a KindType lookup.
	roslynMissingMember = regexp.MustCompile(`'([\w.]+(?:<[^']*>)?)' does not contain a definition for '(\w+)'`)
	// CS1729 / CS7036: `'Widget' does not contain a constructor that takes 2 arguments`. The member
	// list shows the constructors that do exist — the same answer javacConstructorArity buys.
	roslynConstructorArity = regexp.MustCompile(`'([\w.]+(?:<[^']*>)?)' does not contain a constructor that takes`)
	// CS0246 (`The type or namespace name 'X' could not be found`) and CS0234 (`… does not exist in
	// the namespace 'Y'`). The name did not resolve at all, so this is a classpath search for the
	// using directive, not a member dump.
	roslynMissingType = regexp.MustCompile(`type or namespace name '(\w+)(?:<[^']*>)?' (?:could not be found|does not exist)`)
	// CS0103: `The name 'RequestOptions' does not exist in the current context`. Capitalised only,
	// for the reason javacNameIsUnresolvedType gives: a search for a lowercase local is noise that
	// renders to the model as a using suggestion.
	roslynMissingName = regexp.MustCompile(`The name '([A-Z]\w*)' does not exist in the current context`)
	// CS0122: `'Widget.secret()' is inaccessible due to its protection level`. javap and the XML
	// docs list only accessible members, so the dump answers it twice over — it shows the public
	// alternative AND shows the rejected member is not among them.
	roslynInaccessible = regexp.MustCompile(`'([\w.]+)\.(\w+)(?:\([^']*\))?' is inaccessible due to its protection level`)

	// TS2339 (`Property 'reqest' does not exist on type 'Playwright'.`) and TS2551, which is the
	// same message plus tsc's own near-miss suggestion. Worth noting that tsc has ALREADY computed
	// the correction in the TS2551 case; resolving the type surfaces it either way.
	tscMissingProperty = regexp.MustCompile(`Property '(\w+)' does not exist on type '([^']+)'`)
	// TS2304: `Cannot find name 'RequestOptions'.`
	tscCannotFindName = regexp.MustCompile(`Cannot find name '([A-Z]\w*)'`)
	// TS2305 / TS2724: `Module '"playwright"' has no exported member 'APIRequestArgs'.` — the
	// TypeScript spelling of the invented-type failure that stalled run
	// api-e08817ff5df431f6bb8f1fb92e7659a2 in Java.
	tscNoExportedMember = regexp.MustCompile(`has no exported member '(\w+)'`)
)

// tscPlainTypeName matches a type name worth looking up. TS2339 reports the type as written, which
// may be an inline literal (`{ a: string; }`), a union (`A | B`), or an array (`Foo[]`); none of
// those name a declaration the provider can resolve, and passing them through would spend a bounded
// target slot on a guaranteed miss.
var tscPlainTypeName = regexp.MustCompile(`^[A-Za-z_$][\w$.]*(?:<.*>)?$`)

// MaxLookupTargets bounds how many types one round will actually look up on the classpath. A
// cascading compile failure names many types; the first few carry the diagnosis and the rest are
// consequences. The run this was built for would have produced three.
//
// Enforced by CapTargets AFTER the caller's filters run, never here. Truncating during the parse
// spent slots on candidates the filters were about to discard anyway: in run
// api-0c344e6bc0658e0db06506efb9d964f5 the iteration-0 diagnostic named more than ten distinct
// types, the cut at six happened before FilterOwnedTypes / FilterUninterestingTypes /
// DropSymbolsCoveredByType, and five survived — so the budget was underspent while real targets
// (LocatorAssertions#assertThat, PageAssertions#assertThat) never reached a lookup at all.
const MaxLookupTargets = 6

// maxParsedTargets is the wider budget the PARSER keeps, so the filters have candidates to work
// with before CapTargets takes the final six. Bounded because a diagnostic can name hundreds of
// types and every one carries a regexp match and a slice entry.
const maxParsedTargets = 24

// CapTargets truncates a filtered target list to MaxLookupTargets, preserving order — which
// ParseTargets established as diagnostic position, most significant first.
func CapTargets(targets []Target) []Target {
	if len(targets) > MaxLookupTargets {
		return targets[:MaxLookupTargets]
	}
	return targets
}

// staticCallProviders maps a statically-imported call the model got wrong onto the types that
// really declare it, for `no suitable method found`.
//
// An explicit table rather than a classpath member search, for the same reason
// frameworkAnnotationTargets is a list: javap resolves a type name, and the jar index resolves a
// class name — neither can answer "which type declares a method called assertThat that accepts an
// int". A name absent from the classpath resolves to nothing and is dropped silently by Lookup, so
// listing a provider a project does not depend on costs one cache miss and no prompt runes.
//
// Kept to the assertion entry points because those are the receiverless calls a generated test
// makes. A wrong overload on an ordinary receiver already has its type named by
// javacMethodOnVariable.
func staticCallProviders(method string) []string {
	switch method {
	case "assertThat":
		return []string{"org.assertj.core.api.Assertions", "org.hamcrest.MatcherAssert"}
	case "assertEquals", "assertNotEquals", "assertTrue", "assertFalse",
		"assertNull", "assertNotNull", "assertThrows", "assertSame", "assertNotSame":
		return []string{"org.junit.jupiter.api.Assertions"}
	}
	return nil
}

// javacRef is one parsed `symbol:` or `location:` detail line.
type javacRef struct {
	// kind is javac's KindName word: class, interface, enum, record, method, constructor,
	// variable, package, module, "type variable", "record component".
	kind string
	// name is the name javac reported, with any argument list and type arguments stripped.
	name string
	// ofType is the receiver's declared type, set only for the `variable v of type T` shape —
	// where T, not v, is the thing with a member list.
	ofType string
}

// parseJavacRef reads one detail line into its kind and name.
//
// Splitting on the kind word rather than matching a fixed alternation is what makes this total: a
// kind this code has never seen parses into javacRef.kind and is simply classified as "no member
// list to dump", instead of falling off the end of a regex alternation and producing nothing at all.
func parseJavacRef(line string) javacRef {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) == 0 {
		return javacRef{}
	}
	kind, rest := f[0], f[1:]
	// javac's only two multi-word kind names.
	if len(rest) > 0 && ((kind == "type" && rest[0] == "variable") || (kind == "record" && rest[0] == "component")) {
		kind, rest = kind+" "+rest[0], rest[1:]
	}
	ref := javacRef{kind: kind}
	if len(rest) == 0 {
		return ref
	}
	// `variable w of type com.acme.Widget`: the lookup is the TYPE, not the variable.
	for i := 0; i+2 < len(rest); i++ {
		if rest[i] == "of" && rest[i+1] == "type" {
			ref.name = strings.Join(rest[:i], " ")
			ref.ofType = trimTypeArguments(strings.Join(rest[i+2:], " "))
			return ref
		}
	}
	ref.name = trimCallSignature(strings.Join(rest, " "))
	return ref
}

// trimTypeArguments reduces a generic type to the raw type javap and the class index accept:
// `java.util.List<org.acme.Pet>` -> `java.util.List`.
func trimTypeArguments(s string) string {
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// trimCallSignature strips the argument list javac prints after a method or constructor name:
// `jsonBody(java.lang.Class<a.b.OrderResponse>)` -> `jsonBody`.
func trimCallSignature(s string) string {
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	return trimTypeArguments(s)
}

// javacOwnerType returns the type whose member list explains a rejected symbol, or "" when the
// location names something with no members to dump (a package, a module, a type variable).
func javacOwnerType(loc javacRef) string {
	if loc.ofType != "" {
		return loc.ofType
	}
	switch loc.kind {
	case "class", "interface", "enum", "record", "annotation", "@interface":
		return loc.name
	}
	return ""
}

// javacNameIsUnresolvedType reports whether the rejected symbol is plausibly a TYPE the source
// failed to import, which calls for a classpath search rather than a member dump.
func javacNameIsUnresolvedType(sym javacRef) bool {
	switch sym.kind {
	case "class", "interface", "enum", "record", "annotation", "@interface":
		return true
	case "variable":
		// javac writes `variable` for genuine undefined locals as well as for an unimported type
		// used as an expression (`RequestOptions.create()`). Only a Capitalised name is taken: a
		// classpath search for `orderId` burns a bounded slot, and every candidate it resolves is
		// rendered to the model as an import suggestion, which is worse than no answer.
		return startsUpperASCII(sym.name)
	}
	return false
}

func startsUpperASCII(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

// simpleName drops any qualification, which is what the class-index search matches on.
func simpleName(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// ParseTargets derives classpath lookups from a compiler diagnostic, most significant first.
//
// Ordering matters: the caller truncates at MaxLookupTargets, and the primary error is the one worth
// spending the budget on. Targets are ordered by the position of the diagnostic that produced
// them, which for javac output is the order the compiler reported the problems.
func ParseTargets(errorOutput string) []Target {
	return ParseTargetsWithSources(errorOutput, nil)
}

// ParseTargetsWithSources is ParseTargets with the files under repair available, which is what lets
// a failed import be resolved to the simple name it was trying to bind. sources maps repo-relative
// path to content; a nil map degrades to the diagnostic-only behaviour.
func ParseTargetsWithSources(errorOutput string, sources map[string]string) []Target {
	if strings.TrimSpace(errorOutput) == "" {
		return nil
	}
	// Every candidate carries the byte offset of the diagnostic that produced it, and the final
	// list is ordered by that offset before CapTargets truncates — so the budget is spent in the
	// order the compiler reported the problems, which is what the doc comment above has always
	// promised.
	//
	// It used to be spent by diagnostic KIND instead: assertion-call providers first, unresolved
	// names last. In run api-e2d8d10aba45c24e3dd53d2d722a4441 the primary error was
	// `package org.springframework.boot.test.autoconfigure.web.servlet does not exist` on the
	// FIRST line of the log, and all six slots went to assertion helpers named by later cascading
	// errors — four fix rounds running, the one lookup that could answer the primary never ran.
	//
	// A duplicate (kind, name) keeps the occurrence from the earlier diagnostic — its Member and
	// its position — so a symbol first implicated by the primary error cannot be demoted by a
	// repeat mention further down. Ties (several targets from one diagnostic, e.g. the two
	// providers of a receiverless assertThat) keep their insertion order under the stable sort.
	type candidate struct {
		t   Target
		off int
	}
	var cands []candidate
	seen := map[string]int{} // (kind, name) -> index into cands
	add := func(t Target, off int) {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return
		}
		key := string(t.Kind) + "\x00" + t.Name
		if i, ok := seen[key]; ok {
			if off < cands[i].off {
				cands[i] = candidate{t: t, off: off}
			}
			return
		}
		seen[key] = len(cands)
		cands = append(cands, candidate{t: t, off: off})
	}
	// group returns capture group i of an index-form match, "" when it did not participate.
	group := func(m []int, i int) string {
		if 2*i+1 >= len(m) || m[2*i] < 0 {
			return ""
		}
		return errorOutput[m[2*i]:m[2*i+1]]
	}

	// Receiverless assertion calls that did not resolve: the type that CAN take the argument is
	// the answer, and the types javac named — the rejected candidates, or the test class the call
	// was written in — are at best context.
	//
	// Both javac shapes feed the same table, so it does not matter whether the name was partly in
	// scope. staticCallProviders returns nothing for a name outside the assertion entry points, so
	// an ordinary unresolved method contributes nothing here and falls through to the type lookups
	// exactly as before.
	for _, m := range javacNoSuitableMethod.FindAllStringSubmatchIndex(errorOutput, -1) {
		method := group(m, 1)
		for _, fqcn := range staticCallProviders(method) {
			add(Target{Kind: KindType, Name: fqcn, Member: method}, m[0])
		}
	}
	// The `cannot find symbol` grid, parsed once rather than per (symbolKind, locationKind) pair.
	// Emission order within a block matches what the old per-shape loops produced on a tie: the
	// static-import providers first, then the owner type's member list, then the classpath search.
	for _, m := range javacSymbolLine.FindAllStringSubmatchIndex(errorOutput, -1) {
		sym := parseJavacRef(group(m, 1))
		loc := javacRef{}
		if lm := javacLocationAfterSymbol.FindStringSubmatch(errorOutput[m[1]:]); lm != nil {
			loc = parseJavacRef(lm[1])
		}
		// The `cannot find symbol` half of the missing-static-import failure. Which shape javac
		// picks depends on whether SOME overload of the name was in scope: with one imported the
		// overload set exists and nothing is applicable ("no suitable method found", above); with
		// nothing in scope the name does not resolve at all and lands here. The repair is identical
		// — import the type that declares an applicable overload — and both feed the same table.
		// staticCallProviders returns nothing outside the assertion entry points, so an ordinary
		// unresolved method contributes nothing here and falls through to the lookups below.
		if sym.kind == "method" {
			for _, fqcn := range staticCallProviders(sym.name) {
				add(Target{Kind: KindType, Name: fqcn, Member: sym.name}, m[0])
			}
		}
		// A rejected member on a known type: the fix is always "use a member that exists" and the
		// member list settles it outright. Covers a method or a field, on a receiver variable or on
		// a type name, plus a missing nested type or enum constant — all the same lookup.
		if owner := javacOwnerType(loc); owner != "" {
			add(Target{Kind: KindType, Name: owner, Member: sym.name}, m[0])
		}
		// A name that did not resolve at all needs a classpath search for the package it lives in,
		// not a member dump.
		if javacNameIsUnresolvedType(sym) {
			add(Target{Kind: KindSymbol, Name: simpleName(sym.name)}, m[0])
		}
	}
	for _, m := range javacAmbiguous.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 2), Member: group(m, 1)}, m[0])
	}
	for _, m := range javacNotApplicable.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 1), Member: group(m, 2)}, m[0])
	}
	// Construction the model got structurally wrong. `new Pattern("…")` stalled three rounds of one
	// run: Pattern's constructor is private and the API is the static Pattern.compile. javap lists
	// only accessible members, so the dump answers it twice over — it shows compile(...) AND shows
	// no usable constructor at all. Neither shape carries a `symbol:`/`location:` pair, so nothing
	// in this file matched them and the type was never looked up.
	for _, m := range javacConstructorArity.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 2), Member: group(m, 1)}, m[0])
	}
	for _, m := range javacPrivateAccess.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 2), Member: group(m, 1)}, m[0])
	}
	// An import of a package that is not on the classpath. The simple name the model wanted appears
	// nowhere in the diagnostic, so this one needs the sources to recover it.
	for _, m := range javacMissingPackage.FindAllStringSubmatchIndex(errorOutput, -1) {
		if name, ok := missingPackageSymbol(group(m, 1), sources); ok {
			add(Target{Kind: KindSymbol, Name: name}, m[0])
		}
	}

	// ----- Roslyn -----
	for _, m := range roslynMissingMember.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 1), Member: group(m, 2)}, m[0])
	}
	for _, m := range roslynConstructorArity.FindAllStringSubmatchIndex(errorOutput, -1) {
		name := group(m, 1)
		add(Target{Kind: KindType, Name: name, Member: simpleName(name)}, m[0])
	}
	for _, m := range roslynInaccessible.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindType, Name: group(m, 1), Member: group(m, 2)}, m[0])
	}
	for _, m := range roslynMissingType.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindSymbol, Name: group(m, 1)}, m[0])
	}
	for _, m := range roslynMissingName.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindSymbol, Name: group(m, 1)}, m[0])
	}

	// ----- tsc -----
	for _, m := range tscMissingProperty.FindAllStringSubmatchIndex(errorOutput, -1) {
		owner := strings.TrimSpace(group(m, 2))
		if !tscPlainTypeName.MatchString(owner) {
			continue
		}
		add(Target{Kind: KindType, Name: trimTypeArguments(owner), Member: group(m, 1)}, m[0])
	}
	for _, m := range tscNoExportedMember.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindSymbol, Name: group(m, 1)}, m[0])
	}
	for _, m := range tscCannotFindName.FindAllStringSubmatchIndex(errorOutput, -1) {
		add(Target{Kind: KindSymbol, Name: group(m, 1)}, m[0])
	}

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].off < cands[j].off })
	out := make([]Target, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.t)
	}
	if len(out) > maxParsedTargets {
		out = out[:maxParsedTargets]
	}
	return out
}

// javaImportRE captures the simple name bound by a single-type import of a given package. The
// package is quoted into the pattern by importedSimpleName, so it matches only the failing import.
var javaImportLineTemplate = `(?m)^\s*import\s+(?:static\s+)?%s\.(\w+)\s*;`

// missingPackageSymbol turns javac's "package X does not exist" into the simple name to look up on
// the classpath, or reports that no usable name exists.
//
// javac emits this diagnostic for two structurally different mistakes, and the old code handled
// only the first:
//
//	(a) a qualified use of a nested type that is not there — `RuntimeHints.ResourcesRegistry`
//	    yields `package RuntimeHints does not exist`. Here the diagnostic text IS the simple name.
//	(b) an import of a package that is not on the classpath —
//	    `import org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest;` yields
//	    `package org.springframework.boot.test.autoconfigure.web.servlet does not exist`. Here the
//	    simple name the model wanted (WebMvcTest) appears NOWHERE in the diagnostic.
//
// Taking the first dotted segment is right for (a) and useless for (b): it produced the target
// `symbol:org` in round 0 of run api-d4895d20922fd19a9a35fab4ec5dea88, burning one of the bounded
// target slots on a classpath search for a name that cannot exist, while the type that would have
// answered the error — org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest — went
// unresolved. Round 1 only recovered because javac happened to ALSO emit `cannot find symbol: class
// WebMvcTest`, which a different regex matches. That is luck, not design.
//
// Resolution order:
//  1. no dots: shape (a), the text is the name;
//  2. dotted, and a source file imports something from that exact package: shape (b), take the
//     simple name off the import line — this is the only place it is written down;
//  3. dotted with a capitalised final segment: shape (a) written out in qualified form
//     (`a.b.RuntimeHints.Resources`), so the final segment is the enclosing type;
//  4. otherwise: report nothing. An unusable target is worse than no target, because the slot is
//     bounded and every candidate is rendered to the model as an import suggestion.
func missingPackageSymbol(pkg string, sources map[string]string) (string, bool) {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return "", false
	}
	if !strings.Contains(pkg, ".") {
		return pkg, true
	}
	if name := importedSimpleName(pkg, sources); name != "" {
		return name, true
	}
	last := pkg[strings.LastIndex(pkg, ".")+1:]
	if last != "" && last[0] >= 'A' && last[0] <= 'Z' {
		return last, true
	}
	return "", false
}

// importedSimpleName finds the type a source file tried to import from pkg.
func importedSimpleName(pkg string, sources map[string]string) string {
	if len(sources) == 0 {
		return ""
	}
	re, err := regexp.Compile(fmt.Sprintf(javaImportLineTemplate, regexp.QuoteMeta(pkg)))
	if err != nil {
		return ""
	}
	// Deterministic across map iteration order: a diagnostic can name a package that two files
	// import differently, and the audit must not flip between runs.
	paths := make([]string, 0, len(sources))
	for p := range sources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if m := re.FindStringSubmatch(sources[p]); m != nil {
			return m[1]
		}
	}
	return ""
}

// uninterestingTypePrefixes are the language-core packages whose members are both universally known
// and numerous enough to crowd out the rest of the prompt.
//
// The list is deliberately narrow. A first cut covered most of the JDK — java.util, java.io,
// java.time, javax, jakarta — and that was too blunt: a later run stalled three rounds on
// `new Pattern("…")` (Pattern's constructor is private; the API is Pattern.compile), and
// `java.util.regex.Pattern` is exactly the member dump that answers it. Filtering a package because
// it ships with the platform confuses "well known" with "small and obvious".
//
// java.lang is kept because String/Object/Integer really are known to every model and really do
// carry a hundred-plus members. Everything else in the standard library earns its place on the same
// terms as any third-party type, and RankMembers' per-name cap bounds the cost.
var uninterestingTypePrefixes = []string{
	"java.lang.",
	"kotlin.",
}

// IsUninterestingType reports whether a fully-qualified JAVA type is one the model can be assumed
// to know well enough that dumping its members is a waste of prompt budget.
//
// A type whose diagnostic named no member is also uninteresting: without a member to rank against,
// RankMembers has no signal and the dump is an unordered wall of declarations.
//
// Java-specific by definition — see IsUninterestingTypeForLang for why that matters.
func IsUninterestingType(fqcn string) bool {
	return IsUninterestingTypeForLang(LangJava, fqcn)
}

// IsUninterestingTypeForLang is IsUninterestingType with the language it is judging.
//
// The language matters because of the dotless rule. In Java a name with no dot cannot be resolved
// as a type at all — javap needs the package — so dropping it saves a guaranteed miss. In C# and
// TypeScript the opposite is true: roslyn reports `'Widget' does not contain a definition for …`
// with the SIMPLE name, and TypeScript has no package qualification to report, so every type name
// those compilers produce is dotless. Applying Java's rule to them silently discarded every target
// parsed from a C# or TS diagnostic — which, combined with this file having had no C#/TS patterns
// at all, is why those languages never once saw an API-surface block built from their own compiler
// output.
//
// The java.lang / kotlin prefix list stays Java-only for the same reason: it names packages, and
// the other two ecosystems have none.
func IsUninterestingTypeForLang(lang Lang, fqcn string) bool {
	f := strings.TrimSpace(fqcn)
	if f == "" {
		return true
	}
	if NormalizeLang(string(lang)) != LangJava {
		return false
	}
	if !strings.Contains(f, ".") {
		return true
	}
	for _, p := range uninterestingTypePrefixes {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}

// FilterUninterestingTypes drops KindType targets in the JDK/BCL, leaving KindSymbol lookups
// untouched — resolving an unimported simple name to `org.junit.jupiter.api.AfterEach` is exactly
// the import line the fixer needs, even though the type itself is well known.
//
// lang decides whether the dotless rule applies; see IsUninterestingTypeForLang. Pass LangUnknown
// to keep every type target, which is the right default for a language with no such list.
func FilterUninterestingTypes(lang Lang, targets []Target) []Target {
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if t.Kind == KindType && IsUninterestingTypeForLang(lang, t.Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// FilterOwnedTypes drops KindType targets that name a type declared in the repository itself.
// javac's `location:` line is the ENCLOSING class for an unresolved-symbol error, so a naive parse
// asks the classpath about the very test file under repair — a lookup that can only ever miss.
// ownedFQCNs is matched by suffix so a package-qualified name matches a repo-relative source path.
func FilterOwnedTypes(targets []Target, repoTypeNames map[string]bool) []Target {
	if len(repoTypeNames) == 0 {
		return targets
	}
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if t.Kind == KindType && repoTypeNames[t.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// DropSymbolsCoveredByType removes a bare-symbol lookup when a surviving type target already
// explains it — i.e. the diagnostic said "class X, location: Y" and Y is a real third-party type
// whose member list will show what X should have been.
//
// Without this the prompt carries both the useful member dump AND a classpath-wide search for the
// simple name, and the latter is actively misleading: every candidate is rendered as
// "(resolves the unimported symbol; import …)", so the model is handed several imports that cannot
// be right. Call after FilterOwnedTypes, which is what decides whether Y survived.
func DropSymbolsCoveredByType(targets []Target) []Target {
	covered := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.Kind == KindType && t.Member != "" {
			covered[t.Member] = true
		}
	}
	if len(covered) == 0 {
		return targets
	}
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if t.Kind == KindSymbol && covered[t.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// TypeSurface is the extracted API of one classpath type.
type TypeSurface struct {
	// FQCN is the type this surface describes. For a KindSymbol lookup it is the resolved
	// candidate; when several candidates exist they appear as separate surfaces.
	FQCN string
	// Members are rendered declarations, already filtered and ranked. Empty for a symbol-only
	// resolution where the point is the import, not the member list.
	Members []string
	// Truncated is true when Members was capped, so the prompt can say so rather than let the model
	// read absence as proof of non-existence.
	Truncated bool
	// Origin is the jar or directory the type came from, for the audit trail.
	Origin string
	// AllMemberNames is every member name the type declares, complete regardless of what
	// Truncated says about Members.
	//
	// Members is a PROMPT budget: RankMembers cuts it at maxMembersPerType and maxOverloadsPerName
	// so the block stays readable. Truncated describes that cut and nothing else — the underlying
	// javap/d.ts/XML-doc read was complete either way. A caller asking "does this type declare X"
	// must not be answered by the rendering budget, and in run
	// api-f34f51a6e1fb10a79f2f57314aae3d23 it was: LocatorAssertions came back
	// `40 member(s), truncated=true` and PageAssertions `7 member(s), truncated=true` (the
	// per-name overload cap), which disabled the generation-time membership check on every Java
	// Playwright gap. hasStatus and hasHeader shipped to the compiler again.
	//
	// Names only, so carrying it costs a few hundred bytes and never competes with the prompt.
	AllMemberNames []string
}

// NewTypeSurface builds a surface from a complete member list: Members is the ranked, truncated
// view for the prompt, AllMemberNames the complete set for membership questions.
//
// Exported because it carries an invariant a struct literal cannot: a TypeSurface built by hand
// has no AllMemberNames, so every membership question against it answers "no". Providers all live
// in this package, but callers outside it construct surfaces in tests, and a fixture that silently
// disagrees with production is worse than no fixture.
func NewTypeSurface(fqcn string, members []string, wanted, origin string) TypeSurface {
	ranked, truncated := RankMembers(append([]string(nil), members...), wanted)
	names := make([]string, 0, len(members))
	seen := make(map[string]bool, len(members))
	for _, decl := range members {
		n := memberName(decl)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return TypeSurface{FQCN: fqcn, Members: ranked, Truncated: truncated, Origin: origin, AllMemberNames: names}
}

// DeclaresMember reports whether the type declares a member of this name. Answered from the
// complete list, so a truncated prompt view never turns into a claim about existence.
func (s TypeSurface) DeclaresMember(name string) bool {
	for _, n := range s.AllMemberNames {
		if n == name {
			return true
		}
	}
	return false
}

// maxMembersPerType bounds one type's member dump. AssertJ's Assertions has hundreds of overloads;
// shipping all of them would crowd out the file content the model also has to reproduce.
const maxMembersPerType = 40

// maxOverloadsPerName caps how many overloads of a single member name are shown.
//
// This cap is the difference between the feature working and not. AssertJ's Assertions declares
// roughly 150 `assertThat` overloads; ranking purely by relevance to the rejected name puts every
// one of them ahead of `assertThatThrownBy`, which is the actual fix for `assertThat(() -> …)`, and
// the type cap then cuts before the model ever sees it. For a wrong-API error the useful signal is
// the SET OF NAMES the type offers, not an exhaustive overload dump of one of them.
const maxOverloadsPerName = 3

// RankMembers orders a type's members so the ones most likely to be the intended fix come first,
// then truncates. `wanted` is the member the diagnostic rejected.
//
// Two stages. First, name groups are ordered by how close the name is to the one the model wanted —
// a wrong-API error is almost always a near-miss (hasURLContaining -> hasURL,
// assertThat -> assertThatThrownBy), so shared prefixes carry nearly all the signal. Then members
// are taken group by group with a per-name overload cap, so breadth of names survives truncation.
func RankMembers(members []string, wanted string) ([]string, bool) {
	wanted = strings.TrimSpace(wanted)

	type group struct {
		name  string
		score int
		first int
		decls []string
	}
	groups := map[string]*group{}
	var order []*group
	for i, m := range members {
		name := memberName(m)
		g, ok := groups[name]
		if !ok {
			g = &group{name: name, score: nameScore(name, wanted), first: i}
			groups[name] = g
			order = append(order, g)
		}
		g.decls = append(g.decls, m)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].first < order[j].first
	})

	out := make([]string, 0, len(members))
	truncated := false
	for _, g := range order {
		take := g.decls
		if len(take) > maxOverloadsPerName {
			take = take[:maxOverloadsPerName]
			truncated = true
		}
		for _, d := range take {
			if len(out) >= maxMembersPerType {
				return out, true
			}
			out = append(out, d)
		}
	}
	return out, truncated || len(out) < len(members)
}

// nameScore rates how likely `name` is to be what the caller meant by `wanted`.
func nameScore(name, wanted string) int {
	if wanted == "" {
		return 0
	}
	ln, lw := strings.ToLower(name), strings.ToLower(wanted)
	switch {
	case name == wanted:
		return 4
	case strings.HasPrefix(name, wanted) || strings.HasPrefix(wanted, name):
		return 3
	case strings.HasPrefix(ln, lw[:min(len(lw), 4)]):
		return 2
	case strings.Contains(ln, lw):
		return 1
	}
	return 0
}

// memberNameRE pulls the identifier out of a javap declaration line.
var memberNameRE = regexp.MustCompile(`([\w$]+)\s*\(`)

// tsPropertyMemberRE matches a TypeScript property member, whose name precedes the type rather
// than a parameter list: `not: LocatorAssertions;`.
//
// Anchored at the start so it cannot fire on a javap line. javap renders a field as
// `public java.lang.String foo;` — type first, no colon — so no Java declaration begins with
// `identifier:`. Without this, RankMembers groups every TS property under its TYPE name, so two
// unrelated properties of the same type are treated as overloads of one member and the second is
// dropped by the per-name overload cap.
var tsPropertyMemberRE = regexp.MustCompile(`^([A-Za-z_$][\w$]*)\??\s*:`)

func memberName(decl string) string {
	if m := tsPropertyMemberRE.FindStringSubmatch(strings.TrimSpace(decl)); len(m) == 2 {
		return m[1]
	}
	if m := memberNameRE.FindStringSubmatch(decl); len(m) == 2 {
		return m[1]
	}
	f := strings.Fields(strings.TrimSuffix(strings.TrimSpace(decl), ";"))
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
