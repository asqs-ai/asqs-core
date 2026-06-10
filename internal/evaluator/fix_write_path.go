package evaluator

import (
	"os"
	"path/filepath"
	"strings"
)

// pathLooksLikeTestArtifact reports whether rel (repo-relative) is a path where test code may be written.
// Mirrors cmd/qualitybot/run.go looksLikeTestPath plus __tests__/ so Jest/Vitest layouts are recognized.
func pathLooksLikeTestArtifact(rel string, lang string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(rel))
	lang = strings.ToLower(strings.TrimSpace(lang))

	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, ".cy.") {
		return true
	}
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	pl := strings.ToLower(rel)
	if strings.Contains(pl, "/__tests__/") || strings.HasSuffix(pl, "/__tests__") {
		return true
	}

	switch lang {
	case "java":
		if !strings.HasSuffix(base, ".java") {
			return false
		}
		if strings.Contains(base, "test") {
			return true
		}
		return strings.Contains(pl, "src/test/") || strings.Contains(pl, "src/it/")
	case "csharp", "cs":
		if !strings.EqualFold(filepath.Ext(base), ".cs") {
			return false
		}
		lb := strings.ToLower(base)
		pl := strings.ToLower(rel)
		// *Tests.cs / *Test.cs (xUnit/MSTest/NUnit); reject bare "Test.cs".
		if strings.HasSuffix(lb, "tests.cs") {
			return true
		}
		if strings.HasSuffix(lb, "test.cs") && lb != "test.cs" {
			return true
		}
		// Convention: files under a Tests folder or .Tests project segment.
		if strings.Contains(pl, "/tests/") || strings.Contains(pl, "\\tests\\") ||
			strings.HasPrefix(pl, "tests/") || strings.Contains(pl, ".tests/") {
			return true
		}
		// Playwright / .NET E2E: names like AsqsPlaywrightSmokeE2E.cs under .../E2E/ often omit *Test*.cs.
		if strings.Contains(pl, "/e2e/") || strings.HasPrefix(pl, "e2e/") ||
			strings.Contains(pl, "\\e2e\\") {
			return true
		}
		stem := strings.TrimSuffix(lb, ".cs")
		if strings.Contains(stem, "e2e") {
			return true
		}
		return false
	case "javascript", "typescript", "js", "ts":
		if strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".js") ||
			strings.HasSuffix(base, ".jsx") || strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs") {
			if strings.Contains(pl, "/e2e/") || strings.HasPrefix(pl, "e2e/") || strings.Contains(pl, "/cypress/") {
				return true
			}
		}
		return false
	default:
		return strings.Contains(base, "test")
	}
}

// fixOutputPathAllowed returns whether an LLM fix may write to relClean (normalized repo-relative path).
// Only generated artifacts (ArtifactPaths) or paths from pathsToRead that clearly look like test files are writable.
// Also allows paths that look like tests and already exist on disk under RepoPath so E2E / layout mismatches
// (LLM key vs ArtifactPaths string) do not block fixes.
// This blocks applying "fixes" to implementation files (e.g. Strapi lifecycles.ts) when the model returns the wrong JSON key.
func fixOutputPathAllowed(relClean string, opts EvalOptions, pathsToRead []string) bool {
	if relClean == "" {
		return false
	}
	n := normalizePathForFix(relClean)
	for _, a := range opts.ArtifactPaths {
		if normalizePathForFix(a) == n {
			return true
		}
	}
	for _, p := range pathsToRead {
		if normalizePathForFix(p) != n {
			continue
		}
		return pathLooksLikeTestArtifact(p, opts.Lang)
	}
	// LLM may return the on-disk path from test output while ArtifactPaths used a different prefix; allow if it's clearly a test file and exists.
	if pathLooksLikeTestArtifact(relClean, opts.Lang) && strings.TrimSpace(opts.RepoPath) != "" {
		full := filepath.Join(opts.RepoPath, filepath.FromSlash(n))
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}
