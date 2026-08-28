package extendmerge

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Public type name / filename agreement.
//
// Java requires a public type to live in a file named after it, and roslyn conventions expect the
// same. The generator is told the required PATH but was never told this rule, so it routinely
// picked its own class name:
//
//	OwnerControllerE2EIT.java    declares  class OwnerControllerE2ETests
//	WelcomeControllerE2EIT.java  declares  class WelcomeControllerE2ETest
//	VetControllerE2EIT.java      declares  class VetControllerE2ETest
//
// javac rejects all three with "class X is public, should be declared in a file named X.java".
// It recurred in rounds 1 and 3 of one run and cost two to three fixer rounds every time — a
// deterministic, preventable failure being repaired by an LLM.
//
// The repair is renaming the TYPE, never the file: the path is reserved at plan time and tracked in
// the fixer's writable set, so moving it would recreate the plan/generator split-brain that
// TestSuggestedPathAgreesAcrossCallers exists to prevent.

var (
	// javaPublicTypeDeclRE captures a PUBLIC top-level Java type declaration and its name.
	javaPublicTypeDeclRE = regexp.MustCompile(`(?m)^(\s*(?:(?:public|static|final|abstract|sealed|non-sealed|strictfp)\s+)*public(?:\s+(?:static|final|abstract|sealed|non-sealed|strictfp))*\s+(?:class|interface|enum|record)\s+)(\w+)`)
	// javaTopLevelTypeDeclRE captures a top-level Java type declaration at column 0 regardless of
	// visibility, so package-private types are covered too.
	//
	// The original rule was public-only, on the reasoning that javac rejects exactly those. That is
	// true of the COMPILER but not of this system. Spring Petclinic's test classes are all
	// package-private (`class PetControllerTests {`), so when the generator produced
	// `class WelcomeControllerE2ETest` inside WelcomeControllerE2EIT.java the rename never fired —
	// javac accepted the file, and the mismatch instead surfaced downstream as confusing
	// diagnostics naming a class no file is named after, and as a fixer that could not correlate
	// the two. Renaming a package-private top-level type to match its filename is safe (nothing
	// outside the file can reference it by the old name in a freshly generated artifact) and keeps
	// artifact identity consistent for every consumer.
	//
	// Anchored at column 0 so a nested `@Nested class Foo` — indented, and legitimately named for
	// its scenario rather than the file — is never touched.
	javaTopLevelTypeDeclRE = regexp.MustCompile(`(?m)^((?:(?:public|static|final|abstract|sealed|non-sealed|strictfp)\s+)*(?:class|interface|enum|record)\s+)(\w+)`)
	// csharpPublicTypeDeclRE is the C# counterpart.
	csharpPublicTypeDeclRE = regexp.MustCompile(`(?m)^(\s*(?:public|internal)\s+(?:static\s+|sealed\s+|partial\s+|abstract\s+)*(?:class|struct|record|interface)\s+)(\w+)`)
)

// enforcePrimaryTypeName renames a generated file's public type to match its filename.
//
// Returns the (possibly rewritten) content, the old name when a rename happened, and whether
// anything changed. A no-op for languages without the rule, for files whose type already agrees,
// and for content declaring no public top-level type.
func enforcePrimaryTypeName(path, content string) (out string, oldName string, changed bool) {
	var re *regexp.Regexp
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		// Public first so an explicitly public type still wins when a file declares several
		// top-level types; fall back to any top-level declaration for the package-private case.
		if javaPublicTypeDeclRE.MatchString(content) {
			re = javaPublicTypeDeclRE
		} else {
			re = javaTopLevelTypeDeclRE
		}
	case ".cs":
		re = csharpPublicTypeDeclRE
	default:
		return content, "", false
	}
	base := filepath.Base(path)
	want := strings.TrimSuffix(base, filepath.Ext(base))
	if want == "" {
		return content, "", false
	}
	m := re.FindStringSubmatchIndex(content)
	if m == nil {
		return content, "", false
	}
	got := content[m[4]:m[5]]
	if got == want {
		return content, "", false
	}
	// Whole-word replacement across the file, so constructors, `Foo.class` literals and nested
	// self-references travel with the declaration. The old name is this file's own type, so a
	// word-boundary match cannot collide with an unrelated symbol.
	wordRE, err := regexp.Compile(`\b` + regexp.QuoteMeta(got) + `\b`)
	if err != nil {
		return content, "", false
	}
	return wordRE.ReplaceAllString(content, want), got, true
}

// javaPackageDeclRE captures a Java package declaration and its dotted name.
var javaPackageDeclRE = regexp.MustCompile(`(?m)^\s*package\s+([\w.$]+)\s*;`)

// enforceJavaPackageMatchesPath rewrites a generated Java file's package declaration to the one its
// directory implies, when the two disagree.
//
// javac requires the declared package to match the source root-relative directory. The generator is
// given the required path but writes the package from whatever it saw in context, and in run
// api-c3e4a6ea003d0f9b1aeb487b4a8faec6 what it saw was the wrong exemplar: the E2E bootstrap plants
// a smoke test at src/test/java/com/asqs/e2e/AsqsPlaywrightSmokeE2E.java, and the generated
// WelcomeControllerE2EIT.java — living under .../petclinic/system/ — came back declaring
// `package com.asqs.e2e` with a class named WelcomeControllerE2ETest.
//
// Rewriting the DECLARATION rather than moving the file, for the same reason enforcePrimaryTypeName
// renames the type rather than the file: the path is reserved at plan time and tracked in the
// fixer's writable set, so relocating it would split the plan and the generator's view of reality.
//
// Returns the (possibly rewritten) content, the old package, and whether anything changed. A no-op
// when the path carries no recognisable Java source root, when there is no package declaration
// (default package), or when the two already agree.
func enforceJavaPackageMatchesPath(path, content string) (out string, oldPkg string, changed bool) {
	if !strings.EqualFold(filepath.Ext(path), ".java") {
		return content, "", false
	}
	want := javaPackageForPath(path)
	if want == "" {
		return content, "", false
	}
	m := javaPackageDeclRE.FindStringSubmatchIndex(content)
	if m == nil {
		return content, "", false
	}
	got := content[m[2]:m[3]]
	if got == want {
		return content, "", false
	}
	return content[:m[2]] + want + content[m[3]:], got, true
}

// javaPackageForPath derives the package a file's location implies, or "" when the path has no
// recognisable Java source root.
//
// Only the conventional Maven/Gradle roots are recognised. Guessing from an unrecognised layout
// would risk rewriting a correct package to a wrong one, which is worse than leaving it alone: a
// mismatched package is one compile error the fixer can repair, while a corrupted package can send
// the file somewhere nothing references it.
func javaPackageForPath(path string) string {
	p := filepath.ToSlash(strings.TrimSpace(path))
	if p == "" {
		return ""
	}
	roots := []string{
		"src/test/java/",
		"src/main/java/",
		"src/it/java/",
		"src/integrationTest/java/",
	}
	idx, root := -1, ""
	for _, r := range roots {
		if i := strings.Index(p, r); i >= 0 && (idx < 0 || i < idx) {
			idx, root = i, r
		}
	}
	if idx < 0 {
		return ""
	}
	rel := p[idx+len(root):]
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "/" || dir == "" {
		return "" // default package: nothing to enforce
	}
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	for _, s := range segs {
		if s == "" || !isJavaIdentifier(s) {
			return ""
		}
	}
	return strings.Join(segs, ".")
}

func isJavaIdentifier(s string) bool {
	for i, r := range s {
		switch {
		case r == '_' || r == '$':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return s != ""
}
