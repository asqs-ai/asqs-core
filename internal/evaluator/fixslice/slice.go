// Package fixslice produces signature-only slices of source files so the LLM fixer can keep dozens
// of read-only dependency sources in its prompt without paying for their method bodies. The slicer
// is deliberately syntax-lite: it uses line scans and brace counting instead of a real parser, so a
// new language can be added with ~30 lines and the worst failure mode is "we pass the file through
// unchanged" (which is also the fallback when the input does not look like the expected language).
//
// Invariants the slicers uphold across languages:
//
//   - Package / namespace / module declarations are kept verbatim at the top of the output.
//   - Import / using statements are kept verbatim so the LLM can reason about what symbols exist.
//   - Class / interface / struct / enum / record headers and their closing braces are kept.
//   - Class-level, field-level and method-level leading comments are kept (including Javadoc / XML
//     doc / TS doc comments) so the caller can still read intent.
//   - Public and protected method signatures are kept, but their bodies are replaced with either
//     an empty `{ /* body elided */ }` (Java / C#) or a single `;` for TypeScript declarations. For
//     Java / C# interfaces we emit `;` to match the normal syntax.
//   - Private / internal-only helpers are dropped entirely because the LLM is never supposed to
//     call them from the test artefact it is about to edit.
//   - Fields are kept (visibility is preserved); initialisers that span multiple lines are
//     truncated to their declaration line.
//
// The slicer has no knowledge of semantics — it only looks at tokens. Any file that fails to slice
// cleanly (mismatched braces, unexpected tokens) is returned unchanged, and the caller continues
// shipping the full file.
package fixslice

import (
	"path/filepath"
	"strings"
)

// SliceSignatures returns a signature-only version of body for the given language. filePath is used
// only to refine language detection (e.g. a .cs.tmpl file) — callers may pass an empty string.
// Languages: "java", "kotlin" (treated as Java for now), "csharp" / "cs", "typescript" / "ts",
// "javascript" / "js" (shares the TS slicer). Anything else returns body unchanged.
func SliceSignatures(lang, filePath, body string) string {
	if body == "" {
		return body
	}
	switch normaliseLang(lang, filePath) {
	case "java":
		return sliceJava(body)
	case "csharp":
		return sliceCSharp(body)
	case "typescript":
		return sliceTypeScript(body)
	default:
		return body
	}
}

func normaliseLang(lang, filePath string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "java", "kotlin", "kt", "scala":
		return "java"
	case "csharp", "cs", "c#":
		return "csharp"
	case "typescript", "ts", "tsx", "javascript", "js", "jsx":
		return "typescript"
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".java", ".kt", ".kts", ".scala":
		return "java"
	case ".cs":
		return "csharp"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "typescript"
	}
	return ""
}

// elidedBody is the marker emitted in place of a removed method body. It mirrors the wording used
// in the llmfix prompt so the model never confuses an elided body with an empty one.
const elidedBody = "{ /* body elided for fixer context */ }"
