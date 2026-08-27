package apisurface

import "strings"

// Lang is the normalized language a provider is selected for.
type Lang string

const (
	LangUnknown Lang = ""
	LangJava    Lang = "java"
	LangCSharp  Lang = "csharp"
	LangNode    Lang = "node"
)

// NormalizeLang maps the language spellings used across config, gap symbols and workflow input onto
// a single value. Callers pass whatever they hold ("java", "csharp", "cs", "typescript", "ts", …)
// rather than each growing its own switch.
func NormalizeLang(lang string) Lang {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java", "kotlin":
		return LangJava
	case "csharp", "cs", "c#", "dotnet":
		return LangCSharp
	case "javascript", "typescript", "js", "ts", "node", "nodejs":
		return LangNode
	default:
		return LangUnknown
	}
}

// NewProviderForLang returns the API-surface provider for a language, or nil when none exists.
//
// A nil Provider is the documented no-op: EvalOptions.APISurfaceProvider and
// LLMGenerator.APISurface both treat nil as "render no block", so an unsupported language behaves
// exactly as it did before any provider existed. Returning a typed nil from a switch would defeat
// those checks, so the untyped nil here matters.
//
// The three providers read three different sources because the three ecosystems publish their APIs
// differently — compiled .class files behind a Maven classpath (javap), TypeScript .d.ts text in
// node_modules, and NuGet XML documentation beside the assembly. They agree on the Provider
// contract and on TypeSurface, which is what lets the generator and the fixer stay language-blind.
func NewProviderForLang(lang string) Provider {
	switch NormalizeLang(lang) {
	case LangJava:
		return NewJavaProvider()
	case LangCSharp:
		return NewCSharpProvider()
	case LangNode:
		return NewNodeProvider()
	default:
		return nil
	}
}
