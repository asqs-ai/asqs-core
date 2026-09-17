package evaluator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errloc"
	"github.com/asqs/asqs-core/internal/evaluator/errout"
	"github.com/asqs/asqs-core/internal/layout"
)

// writableFixPathsForFailure lists the repo-relative paths the fixer may write for this failure:
// the run's generated artifacts plus every pathsToRead entry that looks like a test file (failing
// candidates and adopted tests, which applyLLMFix has already appended by the time this runs).
func writableFixPathsForFailure(opts EvalOptions, pathsToRead []string) []string {
	out := make([]string, 0, len(opts.ArtifactPaths)+len(pathsToRead))
	seen := make(map[string]bool, len(out))
	add := func(p string) {
		n := normalizePathForFix(p)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, a := range opts.ArtifactPaths {
		add(a)
	}
	for _, p := range pathsToRead {
		if pathLooksLikeTestArtifact(p, opts.Lang) {
			add(p)
		}
	}
	return out
}

// testFailureTouchesWritableScope reports whether a test-step failure names at least one file the
// LLM fixer is allowed to write, or at least one source location the existing scope narrowing can
// work from.
//
// Two matchers, because test runners cite files two ways. ParseFailingTestPaths handles the
// runner-summary shapes (jest/vitest FAIL lines, surefire class stems, bare basenames);
// errloc.ParseLocations / errout.AllCitedRepoPaths handle `path:line` citations.
//
// Deliberately conservative, in two ways. Empty output counts as in scope: there is nothing to
// attribute and the historical behaviour was to try. And output that cites ANY source location
// counts as in scope even when that location is production code: a generated test whose failure
// surfaces inside the code under test is the ordinary repair case. The gate fires only for output
// that is definite and names nothing at all — "No test files found", a runner that crashed before
// collecting a suite — where a fixer round has no legitimate target and, in the motivating run,
// rewrote an unrelated file.
func testFailureTouchesWritableScope(errorOutput string, opts EvalOptions, pathsToRead []string) bool {
	if strings.TrimSpace(errorOutput) == "" {
		return true
	}
	paths := writableFixPathsForFailure(opts, pathsToRead)
	if len(paths) > 0 && len(ParseFailingTestPaths(errorOutput, paths)) > 0 {
		return true
	}
	if len(errloc.ParseLocations(errorOutput)) > 0 {
		return true
	}
	return len(errout.AllCitedRepoPaths(errorOutput, filepath.Clean(opts.RepoPath))) > 0
}

// buildOutputSegments are directory names that hold compiler output or dependencies. A segment
// counts only when no `src` segment precedes it, so `packages/api/src/build/x.test.ts` (a source
// directory that happens to be called build) stays a test path while `packages/api/dist/x.test.js`
// and `app/build/classes/...` do not.
var buildOutputSegments = map[string]bool{
	"node_modules": true, "dist": true, "build": true, "out": true, "target": true,
	"bin": true, "obj": true, "coverage": true, ".next": true, ".nuxt": true, ".output": true,
	".angular": true,
}

// isBuildOutputPath reports whether rel (forward slashes) lives under a build-output or
// dependency directory.
func isBuildOutputPath(rel string) bool {
	for _, seg := range strings.Split(strings.ToLower(rel), "/") {
		switch {
		case seg == "src":
			return false
		case buildOutputSegments[seg]:
			return true
		}
	}
	return false
}

// pathLooksLikeTestArtifact reports whether rel (repo-relative) is a path where test code may be written.
// Mirrors cmd/qualitybot/run.go looksLikeTestPath plus __tests__/ so Jest/Vitest layouts are recognized.
func pathLooksLikeTestArtifact(rel string, lang string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(rel))
	lang = strings.ToLower(strings.TrimSpace(lang))
	// Compiler output and dependencies are never artifacts the fixer may own. asqs-go run
	// api-d01f66ab5b4c58f8e129844d98f8e370 adopted dist/__tests__/asqs-bootstrap-smoke.test.d.ts
	// and its compiled .js from the failure output and spent two rounds asking the LLM to repair a
	// declaration file.
	if isBuildOutputPath(rel) || strings.HasSuffix(base, ".d.ts") {
		return false
	}

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
		// One predicate, shared with the write gate and the extend-merge path. They used to be
		// three, and they disagreed: two rejected FooTest.cs and accepted Contest.cs.
		return layout.IsCSharpTestPath(rel)
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

// vendoredPathSegments name directories holding third-party code this run never authors.
//
// A diagnostic that names a file inside one is reporting a FRAME, not a fault site. In a Vitest /
// jsdom stack trace nearly every frame is inside node_modules, and treating the first of them as
// "the file the failure blames" ended run api-680b618789bc3ad51a2f43c5f269a11b at eval iteration 0
// — primaryUnreachableFailingPath returned node_modules/react-dom/cjs/react-dom.development.js,
// which is real, on disk and unwritable, so the loop stopped with eleven writable artifacts and
// twenty unused fix attempts while the actual defect sat in a generated test.
//
// Deliberately only DEPENDENCY trees, never build output: target/, obj/ and dist/ hold code derived
// from this repository's own sources, so a diagnostic naming one is still evidence about repo code
// and must keep reaching the unwinnable-run guard. `packages/` is excluded from this list for the
// same reason — it is a workspace root in most JS monorepos, not a vendor directory.
var vendoredPathSegments = map[string]bool{
	"node_modules": true,
	"vendor":       true,
}

// pathIsVendored reports whether any segment of a diagnostic path names a vendored tree.
//
// The whole token is rejected rather than only its vendored prefix: resolveDiagnosticPathUnderRepo
// walks suffixes, so node_modules/@scope/pkg/dist/index.js would otherwise keep trying
// dist/index.js and index.js and could resolve one of them onto an unrelated repo file.
func pathIsVendored(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(strings.TrimSpace(rel)), "/") {
		if vendoredPathSegments[seg] {
			return true
		}
	}
	return false
}
