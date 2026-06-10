package indexer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/workspace"
)

// SourceExtensions are file extensions treated as source files for scanning (.java, .cs, .ts, .js, etc.).
var SourceExtensions = map[string]string{
	".java": "java",
	".kt":   "java",
	".cs":   "csharp",
	// ASP.NET Core / Blazor markup: count toward csharp for workflow language (Razor-heavy repos were invisible to nCSharp).
	".cshtml": "csharp",
	".razor":  "csharp",
	".ts":     "typescript",
	".tsx":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".html":   "html",
}

// SkipDirNames are directory (segment) names skipped during scan.
// Note: Do not skip "e2e", "e2e-tests", "__tests__", or "cypress" here — repo scan feeds the index phase; skipping those
// segments drops Java paths like src/test/java/.../e2e/*.java (bootstrap smoke) and Playwright/Cypress spec trees from
// the change set, so advanced JAR output is never persisted and ListGapsE2E stays empty. Aligns with
// tools/js-ts-indexer SKIP_DIRS for the same segment names.
var SkipDirNames = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "public": true, "build": true,
	// .NET SDK output: can contain thousands of generated .js (Blazor, static web assets, bundles), which
	// inflated nJST vs nCSharp and misclassified pure C# repos as JavaScript for workflow language.
	"bin": true, "obj": true,
	// ASP.NET Core static root: committed vendor JS (e.g. jQuery, lodash) dominates GitHub language % and file counts;
	// see https://github.com/jbogard/ContosoUniversityDotNetCore (~300 .js under wwwroot vs ~80 .cs).
	"wwwroot": true,
	"out":     true, "target": true, ".next": true, ".nuxt": true, ".output": true,
	".svelte-kit": true, ".astro": true, "coverage": true,
	"website": true, ".nx": true, ".angular": true, ".turbo": true, ".vite": true,
	".parcel-cache": true, ".cache": true, ".serverless": true,
	"angular": true, "angular-animate": true, "angular-loader": true, "angular-mocks": true,
	"angular-resource": true, "angular-route": true,
	"jquery": true, "bootstrap": true, "html5-boilerplate": true, "stories": true, "storybook": true, ".storybook": true,
}

// IsTypeScriptDeclarationPath reports repo-relative paths that are TypeScript declaration files (.d.ts).
// Ambient declarations are not implementation code and must not be selected for unit or E2E test generation.
func IsTypeScriptDeclarationPath(relPath string) bool {
	p := strings.TrimSpace(filepath.ToSlash(relPath))
	if p == "" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(p), ".d.ts")
}

func pathContainsSegment(relPathLower, seg string) bool {
	if seg == "" {
		return false
	}
	for _, part := range strings.Split(relPathLower, "/") {
		if part == seg {
			return true
		}
	}
	return false
}

// IsLikelyTestSourcePath reports repo-relative paths that should be treated as test/E2E sources for indexing.
// Used by ScanRepoForFiles (files.is_test) and Java minimal/E2E enrichment so integration trees like src/it/java and
// Cypress/Playwright specs under .../e2e/ still join ListSymbolsInTestFiles for ListGapsE2E.
// pathHasTestDirectorySegment is true when a path segment is a conventional test root.
// Avoids treating every path containing the substring "test" (e.g. .../contest/..., .../latest/...) as test code,
// which incorrectly set files.is_test and hid PAGE_ROUTE symbols from ListSymbolsInNonTestFiles.
func pathHasTestDirectorySegment(low string) bool {
	for _, seg := range strings.Split(low, "/") {
		if seg == "" {
			continue
		}
		switch seg {
		case "test", "tests", "testing", "__tests__":
			return true
		}
	}
	return false
}

func IsLikelyTestSourcePath(relPath string) bool {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return false
	}
	relSlash := filepath.ToSlash(relPath)
	low := strings.ToLower(relSlash)
	if pathHasTestDirectorySegment(low) {
		return true
	}
	if strings.Contains(relSlash, "Test") {
		return true
	}
	// Maven Failsafe: .../src/it/java/... (path segment "it", not a substring like "reddit")
	if pathContainsSegment(low, "it") {
		return true
	}
	// Common E2E trees without "test" in the path (cypress/e2e, src/e2e, etc.)
	if strings.Contains(low, "/e2e/") || strings.HasPrefix(low, "e2e/") {
		return true
	}
	base := filepath.Base(relSlash)
	baseLow := strings.ToLower(base)
	ext := filepath.Ext(baseLow)
	nameLow := strings.TrimSuffix(baseLow, ext)
	switch ext {
	case ".java":
		baseOrig := filepath.Base(relSlash)
		if strings.HasSuffix(baseOrig, "Test.java") || strings.HasSuffix(baseOrig, "Tests.java") || strings.HasSuffix(baseOrig, "IT.java") {
			return true
		}
		if strings.HasSuffix(nameLow, "test") || strings.HasSuffix(nameLow, "tests") {
			return true
		}
	case ".kt":
		baseOrig := filepath.Base(relSlash)
		if strings.HasSuffix(baseOrig, "Test.kt") || strings.HasSuffix(baseOrig, "Tests.kt") {
			return true
		}
		if strings.HasSuffix(nameLow, "test") || strings.HasSuffix(nameLow, "tests") {
			return true
		}
	case ".cshtml", ".razor":
		if strings.HasSuffix(nameLow, ".tests") || strings.HasSuffix(nameLow, ".test") {
			return true
		}
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		if nameLow == "test" || nameLow == "tests" {
			return true
		}
		if strings.Contains(baseLow, ".spec.") || strings.Contains(baseLow, ".test.") || strings.Contains(baseLow, ".cy.") {
			return true
		}
		// Match tools/js-ts-indexer isLikelyE2ESpecPath / isTestFilePath so files.is_test stays aligned with E2E_SPEC.
		if strings.Contains(low, "/cypress/") || strings.HasPrefix(low, "cypress/") {
			return true
		}
		if strings.Contains(low, "playwright") {
			return true
		}
		if strings.Contains(baseLow, ".e2e.") || strings.Contains(baseLow, ".e2e-spec.") {
			return true
		}
	}
	return false
}

// ScanRepoForFiles walks repoPath (git root) and returns a FileVersion for each source file (content hash, lang from extension).
// Paths in results are always repo-relative to repoPath (git root), even when monoRepoWorkspace scopes the walk to a subdirectory.
// skipPathPrefixes optionally lists repo-relative path prefixes to skip (e.g. "app/lib"); paths use forward slashes; a prefix skips that folder and everything under it. Use nil or empty for no extra skips.
// monoRepoWorkspace: optional normalized repo-relative prefix (see workspace.NormalizeMonoRepoWorkspace); empty scans the full tree unless monoRepoExtraPaths forces an error.
// monoRepoExtraPaths: optional additional repo-relative roots to scan (e.g. shared "services/base"); only valid when monoRepoWorkspace is set; see workspace.ResolveMonoScanRoots.
func ScanRepoForFiles(repoPath string, repoID string, skipPathPrefixes []string, monoRepoWorkspace string, monoRepoExtraPaths []string) ([]FileVersion, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("repo path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo path is not a directory: %s", abs)
	}

	wsNorm, err := workspace.NormalizeMonoRepoWorkspace(monoRepoWorkspace)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	var walkRoots []string
	if wsNorm != "" || len(monoRepoExtraPaths) > 0 {
		walkRoots, err = workspace.ResolveMonoScanRoots(abs, wsNorm, monoRepoExtraPaths)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
	}
	if len(walkRoots) == 0 {
		walkRoots = []string{""}
	}

	normalizePrefix := func(s string) string {
		return strings.ToLower(strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(s)), "/"))
	}
	var normalizedPrefixes []string
	for _, p := range skipPathPrefixes {
		if n := normalizePrefix(p); n != "" {
			normalizedPrefixes = append(normalizedPrefixes, n)
		}
	}
	shouldSkip := func(relPath string) bool {
		relPath = filepath.ToSlash(relPath)
		relLower := strings.ToLower(relPath)
		for _, prefix := range normalizedPrefixes {
			if relLower == prefix || strings.HasPrefix(relLower, prefix+"/") {
				return true
			}
		}
		return false
	}

	seenPath := make(map[string]struct{})
	var out []FileVersion
	for _, root := range walkRoots {
		walkAbs := abs
		if root != "" {
			walkAbs = filepath.Join(abs, filepath.FromSlash(root))
		}
		err = filepath.Walk(walkAbs, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if path != walkAbs {
					base := filepath.Base(path)
					if strings.HasPrefix(base, ".") || SkipDirNames[strings.ToLower(base)] {
						return filepath.SkipDir
					}
					rel, _ := filepath.Rel(abs, path)
					if shouldSkip(rel) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)
			if shouldSkip(relSlash) {
				return nil
			}
			if _, dup := seenPath[relSlash]; dup {
				return nil
			}
			if IsTypeScriptDeclarationPath(relSlash) {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			lang, ok := SourceExtensions[ext]
			if !ok {
				switch ext {
				case ".vue":
					lang, ok = "typescript", true
				case ".json":
					base := strings.ToLower(filepath.Base(path))
					if base == "openapi.json" || base == "swagger.json" {
						lang, ok = "openapi", true
					}
				case ".yaml", ".yml":
					base := strings.ToLower(filepath.Base(path))
					switch base {
					case "openapi.yaml", "openapi.yml", "swagger.yaml", "swagger.yml":
						lang, ok = "openapi", true
					}
				}
			}
			if !ok {
				return nil
			}
			sha, err := fileContentHash(path)
			if err != nil {
				return err
			}
			seenPath[relSlash] = struct{}{}
			out = append(out, FileVersion{
				Path:   relSlash,
				SHA:    sha,
				Lang:   lang,
				Module: "",
				IsTest: IsLikelyTestSourcePath(relSlash),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func fileContentHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:]), nil
}
