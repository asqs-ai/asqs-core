// Package langid canonicalises the language identifiers that flow through a run.
//
// The same language reaches different components under different spellings. The runner emits
// "csharp"; the metadata store holds "csharp" on symbols but plan options and CLI flags carry "cs";
// a repo inspector may answer "C#". Before this package each consumer wrote its own switch, and the
// switches diverged: retrieval's normalizeLangCode folded cs→csharp while testbootstrap's
// normalizeLangForProfile folded only js/ts, so the same value took different paths depending on
// which package saw it first. runner/profile.ForLang("cs") fell through to the JDK default image,
// and vcs gates compared SupportedLanguages by == against whatever the inspector happened to say.
//
// One table, one spelling, no import cycles: this package depends on nothing.
package langid

import "strings"

// Canonical returns the spelling every component stores and compares against: "csharp", "java",
// "javascript", "typescript", … An unknown identifier is returned lower-cased and trimmed rather
// than blanked, so a language this table does not know still compares equal to itself.
func Canonical(lang string) string {
	k := strings.ToLower(strings.TrimSpace(lang))
	switch k {
	case "cs", "c#", "dotnet":
		return "csharp"
	case "js", "jsx", "mjs", "cjs":
		return "javascript"
	case "ts", "tsx", "mts", "cts":
		return "typescript"
	case "kt":
		return "kotlin"
	default:
		return k
	}
}

// Is reports whether lang names the same language as canonical. Both sides are normalised, so a
// call site holding either spelling is correct. An empty side is never a match: "" is "unknown",
// and treating unknown as equal to unknown has silently enabled language-specific behaviour before.
func Is(lang, canonical string) bool {
	l, c := Canonical(lang), Canonical(canonical)
	return l != "" && c != "" && l == c
}

// IsCSharp is the predicate C#-specific arms should branch on.
func IsCSharp(lang string) bool { return Canonical(lang) == "csharp" }

// IsJava is the predicate Java-specific arms should branch on.
func IsJava(lang string) bool { return Canonical(lang) == "java" }

// IsJSTS covers the JS/TS family, which is one ecosystem for every purpose in this repo (one
// toolchain, one indexer, one test-path rule) even though the index stores the two separately.
func IsJSTS(lang string) bool {
	switch Canonical(lang) {
	case "javascript", "typescript":
		return true
	default:
		return false
	}
}

// aliasTable is the reverse of Canonical: every spelling that folds onto a canonical name. Kept
// beside Canonical so a new alias cannot be added to one without the other.
var aliasTable = map[string][]string{
	"csharp":     {"csharp", "cs", "c#", "dotnet"},
	"javascript": {"javascript", "js", "jsx", "mjs", "cjs"},
	"typescript": {"typescript", "ts", "tsx", "mts", "cts"},
	"kotlin":     {"kotlin", "kt"},
}

// AliasesOf returns every accepted spelling of a language, canonical form first. It exists so a
// contract test can drive a `case "csharp"` site with each alias instead of hard-coding a list that
// drifts from the table. A language with no aliases returns just itself.
func AliasesOf(lang string) []string {
	c := Canonical(lang)
	if c == "" {
		return nil
	}
	if al, ok := aliasTable[c]; ok {
		return append([]string(nil), al...)
	}
	return []string{c}
}
