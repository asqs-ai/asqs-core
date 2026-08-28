package apisurface

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// PregenerateTargets returns the third-party types a generated test will call into, so their real
// member lists can be put in front of the model BEFORE it writes the file rather than after the
// compiler rejects it.
//
// Generation has no diagnostic to mine, so ParseTargets does not apply here. What it has instead is
// a fact known in advance: an E2E test written against a given framework will use that framework's
// assertion API, and that API is the one the model most reliably gets wrong. In run
// api-d4895d20922fd19a9a35fab4ec5dea88 the generator emitted, in a single pass,
// LocatorAssertions.hasTextContaining, APIResponseAssertions.hasStatus and
// APIResponseAssertions.hasHeader — none of which exist. javap against the resolved classpath shows
// APIResponseAssertions has exactly two members; the model invented three.
//
// Scope is deliberately narrow, and narrow in a specific direction: these are types whose SOURCE the
// generator can never see. Retrieval already ships the repo's own dependency source, so adding repo
// types here would spend classpath budget re-stating what the prompt contains. (That has a limit
// worth naming: the same run invented Pet.setNew(boolean) with BaseEntity.java sitting in context,
// so this mechanism is not a general cure for invented APIs — it closes the subset where the model
// is guessing because it genuinely has nothing to read.)
//
// Returns nil for every combination not listed, which is the no-op path: the caller renders no
// block and generation is byte-for-byte what it was before.
func PregenerateTargets(lang, e2eFramework string, isE2E bool) []Target {
	out := append([]Target(nil), frameworkAnnotationTargets(lang)...)
	out = append(out, e2eAssertionTargets(lang, e2eFramework, isE2E)...)
	out = append(out, e2eRequestTargets(lang, e2eFramework, isE2E)...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// frameworkAnnotationTargets returns the test-framework types whose PACKAGE the model gets wrong,
// as opposed to whose members it invents. They apply to unit and E2E gaps alike.
//
// This is a different failure mode from the assertion APIs, and it cost the two runs on record
// their first compile error each:
//
//	run api-d4895d20922fd19a9a35fab4ec5dea88:
//	  package org.springframework.boot.test.autoconfigure.web.servlet does not exist   (@WebMvcTest)
//	run api-c3e4a6ea003d0f9b1aeb487b4a8faec6:
//	  cannot find symbol: class LocalServerPort, location: package org.springframework.boot.web.server
//
// Both are Spring Boot 3 import paths emitted against a Boot 4.0.1 project, where the test
// autoconfiguration was split across modules. The member list is irrelevant here — an annotation
// has none worth showing — but resolving the type prints its real fully-qualified name, and
// RenderSurfaces emits a zero-member surface as an import line, which is exactly the fact needed.
//
// Resolution is by simple name (KindSymbol), because the whole point is that the model does not
// know the package. A name that is genuinely absent from the classpath simply resolves to nothing.
func frameworkAnnotationTargets(lang string) []Target {
	if NormalizeLang(lang) != LangJava {
		return nil
	}
	return []Target{
		{Kind: KindSymbol, Name: "WebMvcTest"},
		{Kind: KindSymbol, Name: "SpringBootTest"},
		{Kind: KindSymbol, Name: "LocalServerPort"},
		{Kind: KindSymbol, Name: "AutoConfigureMockMvc"},
		{Kind: KindSymbol, Name: "MockitoBean"},
	}
}

// ResolveCanonicalImports maps each framework test type's simple name to the fully-qualified name
// this project's compile classpath actually provides.
//
// The bootstrap contract's AvailableImports are package PREFIXES derived from a coordinate table,
// which cannot distinguish Spring Boot 3 from Spring Boot 4: both carry spring-boot-starter-test, so
// both are told org.springframework.boot.test.* is importable, and only one of them has
// org.springframework.boot.test.autoconfigure.web.servlet under it. Asking the classpath removes the
// question — the answer is whatever this project resolves, with no version table to maintain and
// nothing to go stale when the next relocation lands.
//
// A name that resolves to nothing, or to MORE than one class, is omitted. Both are cases where the
// contract would be stating something it does not know, and the contract's whole value is that the
// prompt may call it authoritative.
func ResolveCanonicalImports(ctx context.Context, provider Provider, repoPath, lang string) map[string]string {
	targets := frameworkAnnotationTargets(lang)
	if provider == nil || len(targets) == 0 || strings.TrimSpace(repoPath) == "" {
		return nil
	}
	surfaces, err := provider.Lookup(ctx, repoPath, targets)
	if err != nil || len(surfaces) == 0 {
		return nil
	}
	bySimple := map[string][]string{}
	for _, s := range surfaces {
		fq := strings.TrimSpace(s.FQCN)
		if fq == "" {
			continue
		}
		simple := fq[strings.LastIndex(fq, ".")+1:]
		bySimple[simple] = append(bySimple[simple], fq)
	}
	out := map[string]string{}
	for _, t := range targets {
		got := bySimple[t.Name]
		if len(got) != 1 {
			continue // absent, or ambiguous: say nothing rather than pick.
		}
		out[t.Name] = got[0]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func e2eAssertionTargets(lang, e2eFramework string, isE2E bool) []Target {
	if !isE2E {
		return nil
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(e2eFramework)), "playwright") {
		return nil
	}
	// Each binding names the same four concepts differently, so the lists are written out per
	// language rather than derived. Playwright's Java and .NET bindings both expose a static
	// assertion factory plus three assertion types; the TypeScript binding has no factory type
	// (expect() is a function), so it lists three.
	switch NormalizeLang(lang) {
	case LangJava:
		return []Target{
			{Kind: KindType, Name: "com.microsoft.playwright.assertions.PlaywrightAssertions"},
			{Kind: KindType, Name: "com.microsoft.playwright.assertions.LocatorAssertions"},
			{Kind: KindType, Name: "com.microsoft.playwright.assertions.PageAssertions"},
			{Kind: KindType, Name: "com.microsoft.playwright.assertions.APIResponseAssertions"},
			// The Playwright list alone is a trap, because RenderSurfaces presents it as closed:
			// "If the assertion you want is not listed, it does not exist". An E2E test also has to
			// assert on plain values — a status code, a header string — and Playwright's assertThat
			// takes Page, Locator or APIResponse and nothing else. With no sanctioned home for a
			// value assertion the model writes assertThat(response.status()) and javac answers
			// `no suitable method found for assertThat(int)`; run
			// api-5e5535208f4ba61613f60c345ba9b567 died on exactly that line after eleven rounds.
			//
			// AssertJ ships with spring-boot-starter-test, and RankMembers already anticipates it
			// (maxOverloadsPerName exists because Assertions declares ~150 assertThat overloads),
			// so the dump lands bounded and marked truncated. Projects without it resolve nothing
			// and the block is unchanged.
			{Kind: KindType, Name: "org.assertj.core.api.Assertions"},
		}
	case LangCSharp:
		// .NET assertion types are interfaces (I-prefixed); Assertions is the static factory.
		return []Target{
			{Kind: KindType, Name: "Microsoft.Playwright.Assertions"},
			{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
			{Kind: KindType, Name: "Microsoft.Playwright.IPageAssertions"},
			{Kind: KindType, Name: "Microsoft.Playwright.IAPIResponseAssertions"},
		}
	case LangNode:
		// TypeScript declares these as bare interfaces in playwright's test.d.ts; there is no
		// package-qualified name to give.
		return []Target{
			{Kind: KindType, Name: "LocatorAssertions"},
			{Kind: KindType, Name: "PageAssertions"},
			{Kind: KindType, Name: "APIResponseAssertions"},
		}
	default:
		return nil
	}
}

// e2eRequestTargets returns the types an API-driven E2E test calls to MAKE the request, as opposed
// to the types it calls to assert on the result.
//
// e2eAssertionTargets covers only the assertion half, on the reasoning that assertions are what a
// model most reliably invents. Run api-e08817ff5df431f6bb8f1fb92e7659a2 shows the other half is
// worse, because the model was not guessing there — it was confidently writing the JavaScript
// binding. In one file it produced three errors, none of them an assertion:
//
//	playwright.request              // JS property; Java declares APIRequest request()
//	APIRequestArgs.create()         // no such class exists in any binding
//	response.jsonBody(Class)        // JS has response.json(); Java's APIResponse has neither
//
// The prompt carried the four assertion types and AssertJ, and nothing at all about how to issue a
// request. Five fix rounds and the whole run's output went on a repair that
// `APIRequest request();` states in one line.
//
// Java and C# only, and that is not a hedge — it follows the layer those languages actually test at.
// orchestrator.DefaultRetrievalProfileE2E resolves E2E gaps to the http_api profile for Java and C#
// (Spring and ASP.NET both index API_ROUTE + ROUTE_TO_HANDLER) and to e2e_playwright for JS/TS. A
// Spring Boot service has no UI to drive, so its E2E test IS an HTTP test — both E2E gaps in that
// run were API_ROUTE symbols. TypeScript's default is browser-driven, so it keeps the assertion
// list; its request fixture would be a separate call on separate evidence.
//
// Selection is by (lang, e2eFramework, isE2E) exactly like the lists above, which is what keeps the
// caller's block cacheable: pregenerateAPISurfaceEntry keys on those three and puts the result in
// the SYSTEM prompt, so a per-gap dimension here would drop the prompt-cache hit rate to zero.
func e2eRequestTargets(lang, e2eFramework string, isE2E bool) []Target {
	if !isE2E {
		return nil
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(e2eFramework)), "playwright") {
		return nil
	}
	switch NormalizeLang(lang) {
	case LangJava:
		return []Target{
			// The entry point, and the exact miss: `request()` is a METHOD here and a property in
			// JS. Its member list also shows chromium()/firefox()/webkit() are methods, which is
			// the same mistake one step later.
			{Kind: KindType, Name: "com.microsoft.playwright.Playwright"},
			{Kind: KindType, Name: "com.microsoft.playwright.APIRequest"},
			{Kind: KindType, Name: "com.microsoft.playwright.APIRequestContext"},
			// The builder for a request body. `APIRequestArgs` was invented in its place, twice,
			// and the corrected name still failed for three more rounds because nothing named the
			// package — it is under .options, not alongside the rest.
			{Kind: KindType, Name: "com.microsoft.playwright.options.RequestOptions"},
			// Renders as a complete nine-member list, which is what proves jsonBody() and json()
			// do not exist rather than leaving their absence to be inferred.
			{Kind: KindType, Name: "com.microsoft.playwright.APIResponse"},
		}
	case LangCSharp:
		// .NET mirrors the same four concepts as interfaces. A name absent from the project's
		// NuGet XML documentation resolves to nothing and is dropped silently by Lookup, so a
		// spelling that does not match a given Playwright version costs no prompt runes.
		return []Target{
			{Kind: KindType, Name: "Microsoft.Playwright.IPlaywright"},
			{Kind: KindType, Name: "Microsoft.Playwright.IAPIRequest"},
			{Kind: KindType, Name: "Microsoft.Playwright.IAPIRequestContext"},
			{Kind: KindType, Name: "Microsoft.Playwright.IAPIResponse"},
		}
	default:
		return nil
	}
}

// RenderSurfaces formats resolved surfaces as a prompt block.
//
// Truncation is stated per type for the same reason the fixer's renderer states it: a member list
// reads as exhaustive, so silently cutting one would make absence look like proof of non-existence
// — pushing the model away from a real method instead of toward it.
//
// Returns "" for no surfaces so the caller can append unconditionally.
func RenderSurfaces(surfaces []TypeSurface) string {
	if len(surfaces) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n=== API SURFACE (verbatim from the compile classpath - these are the ONLY members that exist) ===\n")
	b.WriteString("Use ONLY these members on these types. If the assertion you want is not listed, it does not exist: express the check with a member that is listed.\n\n")
	for _, s := range surfaces {
		if len(s.Members) == 0 {
			// A symbol-only resolution: the point is the IMPORT, not the member list. This is how
			// an annotation arrives — @WebMvcTest and @LocalServerPort have no members worth
			// showing, and the fully-qualified name is the entire fact the model is missing.
			b.WriteString(fmt.Sprintf("--- %s ---\n", s.FQCN))
			b.WriteString(fmt.Sprintf("  (import %s — this is the correct package for this type in THIS project)\n\n", s.FQCN))
			continue
		}
		origin := ""
		if strings.TrimSpace(s.Origin) != "" {
			origin = " [" + s.Origin + "]"
		}
		b.WriteString(fmt.Sprintf("--- %s%s ---\n", s.FQCN, origin))
		for _, m := range s.Members {
			b.WriteString("  " + m + "\n")
		}
		if s.Truncated {
			b.WriteString("  ... (member list truncated; absence from this list is NOT proof a member does not exist)\n")
		} else {
			b.WriteString("  (complete member list: a member not shown above does not exist)\n")
		}
		b.WriteString("\n")
	}
	out := b.String()
	// Every surface resolved with zero members: nothing was actually said, so say nothing.
	if !strings.Contains(out, "---") {
		return ""
	}
	return out
}

// maxSignatureTargets bounds how many signature-derived types one gap will look up.
//
// Low on purpose. These targets carry no rejected member — nothing has failed yet — so
// RankMembers has no signal to rank against and each dump lands as up to maxMembersPerType
// unordered declarations (see IsUninterestingType's note on exactly this shape). Three is enough
// for the collaborator types a unit test actually has to drive: the method under test in run
// api-4f92fec6985aee5e4ce48de0041747d2 named one (RuntimeHints).
const maxSignatureTargets = 3

// javaSignatureTypeRE matches a capitalised token in a Java signature — the shape of a type name.
// Mirrors typeNamesFromSignature in internal/intelligence/retrieval, which reads the same
// signature JSON for a different purpose.
var javaSignatureTypeRE = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)

// javaImportRE captures a single-type import and its fully-qualified name. Static and wildcard
// imports are deliberately not matched: neither binds a simple name to one resolvable type.
var javaImportRE = regexp.MustCompile(`(?m)^\s*import\s+([\w.$]+\.[A-Z][\w$]*)\s*;`)

// SignatureTargets returns the third-party types named in ONE symbol's signature, resolved to
// fully-qualified names through that source file's own import block.
//
// This is the per-symbol counterpart of PregenerateTargets, and it exists because the fixed list
// cannot cover the type a given method actually takes. Run api-4f92fec6985aee5e4ce48de0041747d2
// generated a test for
//
//	void registerHints(RuntimeHints hints, ClassLoader classLoader)
//
// with deps_count=0 and no similar test on disk. `hints.resources()` returns ResourceHints, a fact
// visible nowhere in the repository — the index leaves external types unresolved by construction
// (411 unresolved IMPORTS edges that run). The model invented RuntimeHints.ResourcesRegistry and
// five fixer rounds could not talk it out of it. javap answers it in one call, and the import line
// `import org.springframework.aot.hint.RuntimeHints;` is what turns the simple name in the
// signature into something javap can be asked about.
//
// Signature types only, never the whole import block. Imports name everything a file touches, and
// uninterestingTypePrefixes is deliberately narrow (java.lang/kotlin only, so the fixer can still
// dump java.util.regex.Pattern) — so an import-driven list would spend the prompt on 40-member
// dumps of java.util.List and java.time.LocalDate. The signature is the part the test must call.
//
// Returns nil for every non-Java language and for any input it cannot parse, which is the no-op
// path: the caller renders no block and generation is exactly what it was before.
func SignatureTargets(lang, signature, source string) []Target {
	if NormalizeLang(lang) != LangJava {
		return nil
	}
	signature = strings.TrimSpace(signature)
	if signature == "" || strings.TrimSpace(source) == "" {
		return nil
	}
	imports := javaImportsBySimpleName(source)
	if len(imports) == 0 {
		return nil
	}
	var out []Target
	seen := map[string]bool{}
	for _, m := range javaSignatureTypeRE.FindAllStringSubmatch(signature, -1) {
		simple := m[1]
		fqcn, ok := imports[simple]
		if !ok {
			// Not imported: either java.lang, a same-package repo type, or a wildcard import.
			// None of the three is answerable from the import block alone, and guessing a package
			// is what this whole mechanism exists to stop.
			continue
		}
		if seen[fqcn] || IsUninterestingType(fqcn) || isWellKnownContainerType(fqcn) {
			continue
		}
		seen[fqcn] = true
		out = append(out, Target{Kind: KindType, Name: fqcn})
		if len(out) >= maxSignatureTargets {
			break
		}
	}
	return out
}

// wellKnownContainerTypes are the JDK containers whose members every model already knows and whose
// member lists are long enough to crowd out the types it does not.
//
// Scoped to SignatureTargets on purpose — IsUninterestingType stays at java.lang/kotlin, because
// the FIXER wants these dumps: a diagnostic that names java.util.regex.Pattern or a Map overload is
// pointing at the exact member the model got wrong, and RankMembers has a rejected name to rank
// against. Signature targets have no diagnostic and no rejected member, so the same dump arrives
// unranked and speculative.
//
// The cost was measured, not guessed. In run api-3fdd28e8f16a37247fa6494315ff6176 two of the five
// signature lookups went on java.util.List (32 members, truncated) and java.util.Map (31 members,
// truncated) — a container return type and a container parameter — while the lookups that carried
// the run were RuntimeHints (6), BindingResult (13), Model (8), RequestParam (4), PathVariable (3).
//
// Everything else in java.util stays eligible. Filtering a package because it ships with the
// platform confuses "well known" with "small and obvious", which is the mistake
// uninterestingTypePrefixes already documents.
var wellKnownContainerTypes = map[string]bool{
	"java.util.Collection":    true,
	"java.util.List":          true,
	"java.util.ArrayList":     true,
	"java.util.LinkedList":    true,
	"java.util.Map":           true,
	"java.util.HashMap":       true,
	"java.util.LinkedHashMap": true,
	"java.util.TreeMap":       true,
	"java.util.SortedMap":     true,
	"java.util.Set":           true,
	"java.util.HashSet":       true,
	"java.util.LinkedHashSet": true,
	"java.util.TreeSet":       true,
	"java.util.SortedSet":     true,
	"java.util.Queue":         true,
	"java.util.Deque":         true,
	"java.util.ArrayDeque":    true,
	"java.util.Iterator":      true,
	"java.util.Optional":      true,
}

// isWellKnownContainerType reports whether a signature-derived target is a JDK container not worth
// a member dump. java.util.stream is matched by prefix: every type in it is a pipeline the model
// knows and none is small.
func isWellKnownContainerType(fqcn string) bool {
	f := strings.TrimSpace(fqcn)
	return wellKnownContainerTypes[f] || strings.HasPrefix(f, "java.util.stream.")
}

// javaImportsBySimpleName maps each single-type import's simple name to its fully-qualified name.
func javaImportsBySimpleName(source string) map[string]string {
	out := map[string]string{}
	for _, m := range javaImportRE.FindAllStringSubmatch(source, -1) {
		fq := m[1]
		simple := fq[strings.LastIndex(fq, ".")+1:]
		if simple == "" {
			continue
		}
		// First import wins; a file cannot legally bind one simple name twice.
		if _, ok := out[simple]; !ok {
			out[simple] = fq
		}
	}
	return out
}
