package extendmerge

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Import union for extend-existing merges.
//
// The extend path splices a payload INSIDE an existing type's body, and unwrapCompilationUnit
// deliberately discards everything outside that body — package line, imports, the lot. Nothing then
// put the imports back. Every new test method that referenced a symbol the target file did not
// already import produced `cannot find symbol` at compile time, and the fixer had to spend a later
// round rediscovering the import the merge had just thrown away.
//
// That is not a corner case. In run api-d7e0cbece3e9260f73836f5d50d21c96 all 10 unit items and all
// 3 e2e items went down the extend path, and the mechanism is visible with a clean before/after:
// the run appended verify(...)/never(...) calls to PetControllerTests.java with no static imports,
// the compiler flagged lines 315/335/350/371, and two rounds later the fixer added
// `import static org.mockito.Mockito.never;` and `...verify;`. The same mechanism produced
// `cannot find symbol: class Set` / `HashSet` in VetTests.java and the whole
// Playwright/Browser/Page/LocalServerPort/AfterEach cluster in WelcomeControllerTest.java.
//
// Collisions matter as much as the union itself: an import that resolves the symbol but introduces
// an ambiguity has traded one compile failure for a harder one. The per-language rules live in
// unionImports.

// importKind distinguishes the forms an import/using directive can take.
//
// A single bool used to stand in for this, meaning "import static" in Java and "global using" in
// C#. Those are unrelated features, and conflating them left `using static X;` with no
// representation at all — so the C# regex simply did not match it, hoistTopLevelImports did not
// lift it out of the payload, and it was spliced INSIDE the class body, where C# rejects it with
// "A using clause must precede all other elements defined in the namespace". That is the exact
// failure class this file exists to prevent, and it was live for every C# repo.
type importKind int

const (
	// importPlain: `import a.b.C;` / `using A.B;`
	importPlain importKind = iota
	// importStatic: `import static a.b.C.m;` / `using static A.B;`
	importStatic
	// importAlias: C# only — `using X = A.B.C;`
	importAlias
)

func (k importKind) String() string {
	switch k {
	case importStatic:
		return "static"
	case importAlias:
		return "alias"
	default:
		return "plain"
	}
}

var (
	// javaImportLineRE captures one Java import at column 0.
	// group 1 = "static " or "", group 2 = the path (possibly on-demand `.*`).
	javaImportLineRE = regexp.MustCompile(`(?m)^import\s+(static\s+)?([\w.$]+(?:\.\*)?)\s*;`)
	// csharpUsingLineRE captures one C# using directive at column 0.
	// group 1 = "global ", group 2 = "static ", group 3 = alias name, group 4 = target.
	//
	// Column 0 is deliberate: a using STATEMENT or using DECLARATION inside a method body
	// (`using var conn = ...;`) is indented, and matching one would strip a live statement out of
	// the payload. The target also admits generics so `using IntList = List<int>;` round-trips.
	csharpUsingLineRE = regexp.MustCompile(`(?m)^(global\s+)?using\s+(static\s+)?(?:([A-Za-z_]\w*)\s*=\s*)?([\w.]+(?:\s*<[^;>]*>)?(?:\[\])?)\s*;`)
	// javaPackageLineRE locates the package declaration, which imports must follow.
	javaPackageLineRE = regexp.MustCompile(`(?m)^package\s+[\w.$]+\s*;`)
	// csharpNamespaceLineRE matches both namespace forms; usings belong before a block namespace
	// and may sit either side of a file-scoped one.
	csharpNamespaceLineRE = regexp.MustCompile(`(?m)^\s*namespace\s+[\w.]+\s*[;{]`)
	// csharpHeaderLineRE matches lines that may legally precede a using directive: blanks, comments
	// and preprocessor directives. Used to find where a file's header ends.
	csharpHeaderLineRE = regexp.MustCompile(`^\s*(//.*|/\*.*|\*.*|\*/|#\w+.*)?\s*$`)
)

// importDecl is one normalized import/using directive.
type importDecl struct {
	kind importKind
	// global is C#-only: `global using ...`, which applies project-wide (C# 10+).
	global bool
	// alias is the alias name when kind == importAlias.
	alias string
	// path is the imported target, e.g. "java.util.Set", "org.mockito.Mockito.*", "System.Linq".
	path string
}

func (d importDecl) render(ext string) string {
	if strings.ToLower(ext) == ".cs" {
		var b strings.Builder
		if d.global {
			b.WriteString("global ")
		}
		b.WriteString("using ")
		if d.kind == importStatic {
			b.WriteString("static ")
		}
		if d.kind == importAlias && d.alias != "" {
			b.WriteString(d.alias)
			b.WriteString(" = ")
		}
		b.WriteString(d.path)
		b.WriteString(";")
		return b.String()
	}
	if d.kind == importStatic {
		return "import static " + d.path + ";"
	}
	return "import " + d.path + ";"
}

// key is the dedupe identity: two decls with the same key are the same directive.
func (d importDecl) key() string {
	return fmt.Sprintf("%v\x00%s\x00%s\x00%s", d.global, d.kind, d.alias, d.path)
}

// onDemandPrefix returns the package/type an on-demand import covers, and whether it is on-demand.
// Java only — C# has no `.*` wildcard form. `java.util.*` -> "java.util".
func (d importDecl) onDemandPrefix() (string, bool) {
	if !strings.HasSuffix(d.path, ".*") {
		return "", false
	}
	return strings.TrimSuffix(d.path, ".*"), true
}

// parseImports extracts the import/using declarations from a compilation unit.
func parseImports(src, ext string) []importDecl {
	var out []importDecl
	seen := map[string]bool{}
	add := func(d importDecl) {
		d.path = strings.TrimSpace(d.path)
		d.alias = strings.TrimSpace(d.alias)
		if d.path == "" || seen[d.key()] {
			return
		}
		seen[d.key()] = true
		out = append(out, d)
	}
	switch strings.ToLower(ext) {
	case ".java":
		for _, m := range javaImportLineRE.FindAllStringSubmatch(src, -1) {
			k := importPlain
			if strings.TrimSpace(m[1]) != "" {
				k = importStatic
			}
			add(importDecl{kind: k, path: m[2]})
		}
	case ".cs":
		for _, m := range csharpUsingLineRE.FindAllStringSubmatch(src, -1) {
			d := importDecl{
				global: strings.TrimSpace(m[1]) != "",
				alias:  strings.TrimSpace(m[3]),
				path:   m[4],
			}
			switch {
			case strings.TrimSpace(m[2]) != "":
				d.kind = importStatic
			case d.alias != "":
				d.kind = importAlias
			default:
				d.kind = importPlain
			}
			add(d)
		}
	}
	return out
}

// unionImports merges payload imports into the target's, returning the imports to ADD (in a stable
// order) and the ones deliberately skipped with the reason.
//
// Skips fall into two classes: redundant (already covered, so adding is noise) and unsafe (adding
// would introduce an ambiguity the compiler rejects). The unsafe rules are language-specific:
//
//   - Java: an on-demand import (`import static a.b.C.*;`) beside a matching single import
//     (`import static a.b.C.m;`) makes javac report every shared member as ambiguous —
//     "both method <T>assertThat(T) in Assertions and ... in Assertions match" — a message naming
//     the same class on both sides that reads like a duplicate-jar problem.
//   - C#: a second `using X = ...;` for an alias name already bound is CS1537, a hard error
//     ("The using alias 'X' appeared previously in this namespace"). A local `using` duplicating a
//     `global using` is CS8933, a warning, so it is skipped as redundant rather than unsafe.
//
// One C# ambiguity is deliberately NOT modelled: two `using static` directives exposing the same
// member name resolve to CS0121 at the USE site, which cannot be decided from the directive lines
// alone. Pretending otherwise would mean refusing safe imports on a guess.
func unionImports(existing, incoming []importDecl, ext string) (add []importDecl, skipped map[string]string) {
	skipped = map[string]string{}
	have := make(map[string]bool, len(existing))
	// onDemand: scopeKey(prefix) of every `x.y.*` already imported (Java).
	onDemand := map[string]bool{}
	// singles: scopeKey(prefix) of every single import already present, so an incoming on-demand
	// import can be recognised as colliding with it (Java).
	singles := map[string]bool{}
	// aliases: alias names already bound (C#).
	aliases := map[string]string{}
	// simpleNames: simple name -> path, for every Java single-type import already present. See the
	// collision check in the incoming loop for why only this shape qualifies.
	simpleNames := map[string]string{}
	// globals / locals: plain paths imported project-wide vs in-file (C#).
	globals := map[string]bool{}
	locals := map[string]bool{}

	for _, d := range existing {
		have[d.key()] = true
		if d.kind == importAlias && d.alias != "" {
			aliases[d.alias] = d.path
		}
		if d.global {
			globals[d.path] = true
		} else {
			locals[d.path] = true
		}
		if p, ok := d.onDemandPrefix(); ok {
			onDemand[scopeKey(d, p)] = true
			continue
		}
		if idx := strings.LastIndex(d.path, "."); idx > 0 {
			singles[scopeKey(d, d.path[:idx])] = true
		}
		if name, ok := javaSingleTypeSimpleName(d, ext); ok {
			simpleNames[name] = d.path
		}
	}

	for _, d := range incoming {
		if have[d.key()] {
			skipped[d.render(ext)] = "already imported"
			continue
		}
		// C#: a repeated alias name is CS1537 whether or not the target matches.
		if d.kind == importAlias && d.alias != "" {
			if target, bound := aliases[d.alias]; bound {
				skipped[d.render(ext)] = fmt.Sprintf("alias %q is already bound to %s (CS1537)", d.alias, target)
				continue
			}
		}
		// C#: a local using that a global using already covers is redundant (CS8933), and the
		// reverse would make every other file's local using redundant.
		if strings.ToLower(ext) == ".cs" && d.kind != importAlias {
			if !d.global && globals[d.path] {
				skipped[d.render(ext)] = "already covered by a global using of " + d.path
				continue
			}
			if d.global && locals[d.path] {
				skipped[d.render(ext)] = "a local using of " + d.path + " is already present"
				continue
			}
		}
		// Java: two single-type imports binding the SAME simple name to different types is a
		// compile-time error (JLS 7.5.1), so an incoming one can never be the right answer when the
		// file already binds that name. The target file compiled before this run touched it, which
		// makes the existing import the one with evidence behind it.
		//
		// This is the extend-path half of a real failure. PetControllerTests.java already carried
		// `import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;` — correct for
		// Spring Boot 4, and why the baseline compiled. Generation returned the Boot 3 spelling
		// (`org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest`) from model memory,
		// the union added it alongside the existing one, and javac reported the added line as
		// `package ... does not exist` for two full fixer rounds.
		if name, ok := javaSingleTypeSimpleName(d, ext); ok {
			if bound, exists := simpleNames[name]; exists && bound != d.path {
				skipped[d.render(ext)] = fmt.Sprintf(
					"the simple name %s is already imported from %s; two single-type imports of one name do not compile (JLS 7.5.1)", name, bound)
				continue
			}
		}
		if p, ok := d.onDemandPrefix(); ok {
			if singles[scopeKey(d, p)] {
				skipped[d.render(ext)] = "would collide with an existing single import from " + p
				continue
			}
		} else if prefix := parentOf(d.path); prefix != "" && onDemand[scopeKey(d, prefix)] {
			skipped[d.render(ext)] = "already covered by an on-demand import of " + prefix
			continue
		}
		have[d.key()] = true
		if name, ok := javaSingleTypeSimpleName(d, ext); ok {
			simpleNames[name] = d.path
		}
		if d.kind == importAlias && d.alias != "" {
			aliases[d.alias] = d.path
		}
		if d.global {
			globals[d.path] = true
		} else {
			locals[d.path] = true
		}
		add = append(add, d)
	}
	sort.Slice(add, func(i, j int) bool {
		if add[i].global != add[j].global {
			return add[i].global
		}
		if add[i].kind != add[j].kind {
			return add[i].kind < add[j].kind
		}
		return add[i].path < add[j].path
	})
	return add, skipped
}

// javaSingleTypeSimpleName returns the simple type name a Java single-type import binds.
//
// Only `import a.b.C;` qualifies, and the exclusions are what keep the collision rule sound:
//
//   - C# is excluded outright: `using A.B;` imports a NAMESPACE and binds no simple name, so two
//     usings ending in the same segment are ordinary correct code. Alias collisions (CS1537) are
//     already handled separately above.
//   - on-demand imports (`a.b.*`) bind nothing eagerly — JLS 7.5.2 resolves them at use site and
//     lets a single-type import shadow them, which the existing singles/onDemand checks model.
//   - static imports are excluded because static members OVERLOAD: `import static a.B.of;` and
//     `import static c.D.of;` together are legal, and ambiguity (if any) is a use-site error.
func javaSingleTypeSimpleName(d importDecl, ext string) (string, bool) {
	if strings.ToLower(ext) != ".java" {
		return "", false
	}
	if d.kind != importPlain {
		return "", false
	}
	if _, onDemand := d.onDemandPrefix(); onDemand {
		return "", false
	}
	idx := strings.LastIndex(d.path, ".")
	if idx <= 0 || idx == len(d.path)-1 {
		return "", false
	}
	return d.path[idx+1:], true
}

func parentOf(path string) string {
	if idx := strings.LastIndex(path, "."); idx > 0 {
		return path[:idx]
	}
	return ""
}

// scopeKey namespaces a prefix by the directive shape it belongs to, so a static and a plain import
// of the same package are never confused for one another.
func scopeKey(d importDecl, s string) string {
	return fmt.Sprintf("%v\x00%s\x00%s", d.global, d.kind, s)
}

// mergeImportsIntoFile inserts add into src's import block and returns the updated source.
//
// Insertion is line-based and always lands at a line boundary; the previous byte-offset form
// concatenated onto whatever preceded it, which for a C# file with a licence header and no usings
// produced `using System.Linq;// Copyright header` on one line.
//
// Anchor chain, first match wins:
//
//  1. the line after the last existing top-level import/using;
//  2. Java: the line after `package a.b;` — C#: the line after a file-scoped `namespace N;`
//     (using directives may legally follow it) or before a block `namespace N {`;
//  3. C# only: the end of the leading header (comments, blank lines, preprocessor directives),
//     which is the compilation-unit position and always legal;
//  4. no anchor — fail closed.
//
// Failing closed matters more than placing the import somewhere plausible: a merge known to be
// missing an import compiles no better than no merge, and it costs the fixer a round to rediscover.
// Java has always failed closed here; C# used to insert at byte 0 and report success.
func mergeImportsIntoFile(src string, add []importDecl, ext string) (string, bool) {
	if len(add) == 0 {
		return src, true
	}
	ext = strings.ToLower(ext)
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	insertLine, ok := importInsertLine(lines, ext)
	if !ok {
		return src, false
	}
	rendered := make([]string, 0, len(add))
	for _, d := range add {
		rendered = append(rendered, d.render(ext))
	}
	out := make([]string, 0, len(lines)+len(rendered))
	out = append(out, lines[:insertLine]...)
	out = append(out, rendered...)
	out = append(out, lines[insertLine:]...)
	return strings.Join(out, "\n"), true
}

// importInsertLine returns the line index new directives should be inserted BEFORE.
func importInsertLine(lines []string, ext string) (int, bool) {
	lineRE := javaImportLineRE
	if ext == ".cs" {
		lineRE = csharpUsingLineRE
	}
	// 1. after the last existing directive.
	last := -1
	for i, ln := range lines {
		if lineRE.MatchString(ln) {
			last = i
		}
	}
	if last >= 0 {
		return last + 1, true
	}
	// 2. after the package / namespace declaration.
	if ext == ".java" {
		for i, ln := range lines {
			if javaPackageLineRE.MatchString(ln) {
				return i + 1, true
			}
		}
		// 4. Java with no package line and no imports: no confident anchor.
		return 0, false
	}
	for i, ln := range lines {
		if !csharpNamespaceLineRE.MatchString(ln) {
			continue
		}
		// A file-scoped `namespace N;` may be followed by using directives; a block
		// `namespace N {` may not, so go before it.
		if strings.HasSuffix(strings.TrimSpace(ln), ";") {
			return i + 1, true
		}
		return i, true
	}
	// 3. C# with no namespace: after the leading header, which is the compilation-unit position.
	for i, ln := range lines {
		if csharpHeaderLineRE.MatchString(ln) {
			continue
		}
		return i, true
	}
	// Header-only file: nothing to extend, so there is no correct place for a using directive.
	return 0, false
}

// hoistTopLevelImports removes column-0 import/using lines from a payload and returns them
// alongside the remainder.
//
// This is what lets a methods-only payload declare its own imports. The generation contract used to
// forbid them outright ("no import or using lines"), which is correct about where they may END UP —
// spliced inside a class body an import is a syntax error — but left the model no way to say "these
// new tests need java.util.Set". It complied, and the merge produced `cannot find symbol` instead.
// Now the contract asks for them at the top and the writer lifts them into the file's real import
// block.
//
// Stripping before classification also keeps classifyExtendPayload honest: a members-only payload
// that carries imports would otherwise be read as a full compilation unit and sent to
// unwrapCompilationUnit, which would find no type body and drop the write entirely. A genuine
// compilation unit still classifies correctly afterwards, on its package line or column-0 type
// declaration.
func hoistTopLevelImports(path, payload string) (imports []importDecl, remainder string) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".java" && ext != ".cs" {
		return nil, payload
	}
	s := strings.ReplaceAll(payload, "\r\n", "\n")
	imports = parseImports(s, ext)
	if len(imports) == 0 {
		return nil, s
	}
	lineRE := javaImportLineRE
	if ext == ".cs" {
		lineRE = csharpUsingLineRE
	}
	kept := make([]string, 0, 32)
	for _, ln := range strings.Split(s, "\n") {
		if lineRE.MatchString(ln) {
			continue
		}
		kept = append(kept, ln)
	}
	return imports, strings.TrimLeft(strings.Join(kept, "\n"), "\n")
}

// describeSkippedImports renders the skip map deterministically for operator output.
func describeSkippedImports(skipped map[string]string) string {
	if len(skipped) == 0 {
		return ""
	}
	keys := make([]string, 0, len(skipped))
	for k := range skipped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (%s)", k, skipped[k]))
	}
	return strings.Join(parts, "; ")
}

// --- Payload symbol analysis (extend path) ---
//
// The import union can only reason about imports the payload DECLARED. Two failures live in the gap
// between that and the imports the payload NEEDS, and both were live in the run of 2026-08-29:
//
//  1. A name the payload uses and never imports. `payloadImports` is built from literal `import …;`
//     lines (hoistTopLevelImports), so an annotation the model wrote without an import produced
//     `cannot find symbol` on a file that had just been merged: @AfterEach, @AfterAll, @DisplayName,
//     @Order and @LocalServerPort across four files in that one run.
//  2. A name the payload uses that an on-demand import was supposed to supply, but which the target
//     already binds from somewhere else. Under JLS 7.5 a single-type import SHADOWS an on-demand
//     one, so `import com.microsoft.playwright.*;` merged into a file already carrying
//     `import org.springframework.data.domain.Page;` left every Playwright `page.navigate(...)`
//     resolving to Spring's Page — eight diagnostics from one silently-wrong binding.

// javaAnnotationUseRE matches an annotation USE: `@Name`, `@Name(...)`, `@Name.Nested`.
//
// The leading capital is what keeps Javadoc out: `@param`, `@return`, `@throws` are lower-case, so
// requiring [A-Z] excludes the entire tag vocabulary without needing to strip comments. An
// annotation named inside a line comment can still match, which costs at most one redundant import
// for a name that resolves — and nothing at all for one that does not.
var javaAnnotationUseRE = regexp.MustCompile(`@([A-Z][A-Za-z0-9_]*)`)

// javaLangAnnotations are importable from nowhere: java.lang is implicitly imported, so emitting an
// import for one is pure noise even though it would resolve.
var javaLangAnnotations = map[string]bool{
	"Override": true, "Deprecated": true, "SuppressWarnings": true,
	"SafeVarargs": true, "FunctionalInterface": true,
}

// payloadAnnotationNames returns the distinct annotation simple names a payload uses.
//
// Annotations only, deliberately. They are the one reference shape whose syntax marks it
// unambiguously as a type use — `@Name` cannot be a variable, a method, or a field — so extracting
// them needs no parser and cannot mistake an identifier for a type. Every unresolved symbol in the
// run this exists to fix was an annotation. Widening to bare type references wants a real parser.
func payloadAnnotationNames(payload string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range javaAnnotationUseRE.FindAllStringSubmatch(payload, -1) {
		name := m[1]
		if javaLangAnnotations[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// usesSimpleName reports whether payload references name as a word, so `Page` does not match
// `Pageable` or `PageImpl`.
func usesSimpleName(payload, name string) bool {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(payload)
}

// unresolvedPayloadAnnotations returns the annotation names in payload that no single-type import
// in scope binds — neither the target's nor the payload's own.
//
// An on-demand import in scope is deliberately NOT a reason to stay silent, though the first draft
// of this treated it as one. The caller only ever adds a name its resolver mapped to EXACTLY ONE
// type on the whole compile classpath, and if only one type has that simple name then a wildcard
// could not have supplied a different one — so the explicit import either duplicates what the
// wildcard would have bound, or supplies what it could not. Both are safe, and a single-type import
// shadows an on-demand one (JLS 7.5), so the explicit spelling wins deterministically.
//
// Staying silent instead was measurably wrong: all three files broken by missing annotation imports
// in the run of 2026-08-29 also carried `import com.microsoft.playwright.*;`, so a wildcard bail-out
// would have skipped inference on 100% of the cases it exists to repair.
func unresolvedPayloadAnnotations(payload string, existing, incoming []importDecl) []string {
	bound := map[string]bool{}
	for _, set := range [][]importDecl{existing, incoming} {
		for _, d := range set {
			if name, ok := javaSingleTypeSimpleName(d, ".java"); ok {
				bound[name] = true
			}
		}
	}
	var out []string
	for _, name := range payloadAnnotationNames(payload) {
		if !bound[name] {
			out = append(out, name)
		}
	}
	return out
}

// shadowedByExistingSingleImport reports names the payload uses that an on-demand import in
// addImports really would have supplied, but which the target already binds by a single-type import
// from a different package.
//
// Adding the wildcard is legal and silent — javac reports nothing about the import line itself —
// and then every use of the name resolves to the wrong type. That asymmetry is why the caller fails
// closed on a hit: refusing the merge costs one gap's coverage, while accepting it costs the whole
// module's compile and, as measured, the fixer's entire attempt budget chasing diagnostics whose
// cause is an import that looks correct.
//
// typeExists is what keeps this from firing on coincidences, and it is required: a name the payload
// uses which the target already imports is the NORMAL case, not the hazard. A payload adding
// `java.util.*` while using @Test in a file importing org.junit.jupiter.api.Test is fine, because
// java.util.Test does not exist; the same shape with com.microsoft.playwright.* and Page is not,
// because com.microsoft.playwright.Page does. Without a way to ask, we cannot tell the two apart, so
// a nil or unanswering typeExists reports no hazard rather than refusing every wildcard.
func shadowedByExistingSingleImport(payload string, existing, addImports []importDecl, typeExists func([]string) map[string]bool) map[string]string {
	if typeExists == nil {
		return nil
	}
	var wildcards []string
	for _, d := range addImports {
		if p, onDemand := d.onDemandPrefix(); onDemand && d.kind == importPlain {
			wildcards = append(wildcards, p)
		}
	}
	if len(wildcards) == 0 {
		return nil
	}
	// Candidate FQNs: for every name the payload uses that the target binds from elsewhere, the
	// name as the wildcard's package would spell it.
	candidates := map[string]string{} // fqn -> existing import path that would shadow it
	names := map[string]string{}      // fqn -> simple name
	for _, d := range existing {
		name, ok := javaSingleTypeSimpleName(d, ".java")
		if !ok || !usesSimpleName(payload, name) {
			continue
		}
		for _, prefix := range wildcards {
			// Same package: the single import and the wildcard name the same type, so the single
			// import winning is harmless.
			if parentOf(d.path) == prefix {
				continue
			}
			fqn := prefix + "." + name
			candidates[fqn] = d.path
			names[fqn] = name
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	ask := make([]string, 0, len(candidates))
	for fqn := range candidates {
		ask = append(ask, fqn)
	}
	sort.Strings(ask)
	present := typeExists(ask)
	out := map[string]string{}
	for fqn, shadowedBy := range candidates {
		if present[fqn] {
			out[names[fqn]] = shadowedBy
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// describeShadowedNames renders shadowedByExistingSingleImport's result deterministically.
func describeShadowedNames(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (already imported from %s)", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
