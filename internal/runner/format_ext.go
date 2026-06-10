package runner

import "strings"

// FormatExtensionsForLang returns file extensions commonly formatted for a language.
// Used by format_only_added when the formatter is invoked once per file.
// Nil/empty means "do not filter by extension" (caller may pass nil to RunFormatCommandFiles).
func FormatExtensionsForLang(lang string) []string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java":
		return []string{".java"}
	case "csharp", "cs":
		return []string{".cs"}
	case "go", "golang":
		return []string{".go"}
	case "python", "py":
		return []string{".py"}
	case "javascript", "js":
		return []string{".js", ".jsx", ".mjs", ".cjs"}
	case "typescript", "ts":
		return []string{".ts", ".tsx", ".mts", ".cts"}
	default:
		return nil
	}
}
