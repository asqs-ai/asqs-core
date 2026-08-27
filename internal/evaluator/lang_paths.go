package evaluator

import (
	"path/filepath"
	"strings"
)

// langSourceExtensions maps a run language to the source extensions that can legitimately appear in
// its fix context. Languages absent from the map are not filtered.
var langSourceExtensions = map[string]map[string]bool{
	"java":       {".java": true, ".kt": true, ".xml": true, ".gradle": true, ".kts": true, ".properties": true, ".yml": true, ".yaml": true},
	"csharp":     {".cs": true, ".csproj": true, ".props": true, ".config": true, ".json": true},
	"cs":         {".cs": true, ".csproj": true, ".props": true, ".config": true, ".json": true},
	"typescript": {".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".json": true},
	"javascript": {".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".json": true},
	"go":         {".go": true, ".mod": true, ".sum": true},
}

// splitWrongLanguagePaths partitions best-effort context paths into those whose extension does not
// belong to the run's language and the rest.
//
// A C# run was observed requesting a Java path (src/test/java/com/asqs/e2e/AsqsPlaywrightSmokeE2E.java)
// and reporting it as "not_found" — indistinguishable in the audit from a genuinely missing repo
// file, when the real cause is stale cross-language state. Unknown languages and unknown extensions
// are never filtered: the goal is to label an obvious mismatch, not to police the path set.
func splitWrongLanguagePaths(lang string, paths []string) (wrongLang, other []string) {
	exts, known := langSourceExtensions[strings.ToLower(strings.TrimSpace(lang))]
	if !known {
		return nil, paths
	}
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(strings.TrimSpace(p)))
		// Only classify extensions we recognise as belonging to *some* language; anything else
		// (no extension, .md, .txt) stays in the ordinary not-found bucket.
		if ext != "" && !exts[ext] && extensionBelongsToAnyKnownLang(ext) {
			wrongLang = append(wrongLang, p)
			continue
		}
		other = append(other, p)
	}
	return wrongLang, other
}

func extensionBelongsToAnyKnownLang(ext string) bool {
	for _, exts := range langSourceExtensions {
		if exts[ext] {
			return true
		}
	}
	return false
}
