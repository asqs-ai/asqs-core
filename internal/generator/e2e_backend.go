package generator

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// localOriginRE matches an http(s) origin on this machine with an explicit port, which is how a
// front-end repository spells "the API I talk to" in a development environment file.
var localOriginRE = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0):(\d{2,5})`)

// Bounds on the scan. A front-end repository names its API in a handful of small configuration
// files; walking the whole tree to find them would cost more than the answer is worth.
const (
	e2eBackendMaxFilesScanned = 400
	e2eBackendMaxFileBytes    = 64 * 1024
	e2eBackendMaxOriginsShown = 6
)

// e2eBackendSkipDirs are trees whose localhost literals belong to somebody else's tooling.
var e2eBackendSkipDirs = map[string]bool{
	"node_modules": true, "dist": true, "build": true, "out": true, "coverage": true,
	".git": true, ".angular": true, ".next": true, ".nuxt": true, "vendor": true,
	"e2e": true, "cypress": true, "test-results": true, "playwright-report": true,
}

// e2eBackendScanExts are the file types that carry a configured API origin.
var e2eBackendScanExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".json": true, ".env": true,
}

// unservedAPIOrigins returns the local HTTP origins the application talks to that nothing will be
// serving while the E2E suite runs, sorted and de-duplicated.
//
// The E2E web server starts the APPLICATION and nothing else. An app configured against a separate
// API port therefore runs against a dead socket for the whole suite: in run
// api-620c78444155f43a6afdc9587c097eae the fixture pointed at http://localhost:4201/api with nothing
// on 4201, so every request failed on the next tick. That is what made the remaining failures
// unwinnable rather than merely wrong — `loading` was true for microseconds because the request
// failed immediately, and the results list never populated, so a `<ul>` that is always in the DOM
// was always empty and therefore always reported hidden.
//
// appPort is excluded: that origin IS served. A dev-server proxy is treated as covering everything,
// because a proxied call leaves on the app's own port and the app port is already excluded.
func unservedAPIOrigins(repoPath string, appPort int) []string {
	root := strings.TrimSpace(repoPath)
	if root == "" {
		return nil
	}
	if hasDevServerProxy(root) {
		return nil
	}
	found := map[string]bool{}
	scanned := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is not worth failing the block over
		}
		if d.IsDir() {
			// Hidden directories are editor and CI tooling, not application source. `.vscode/
			// launch.json` in the fixture names Karma's port 9876, which the application never
			// calls — a false origin in this notice is worse than a missing one, because it tells
			// the model to intercept traffic that does not exist.
			if p != root && (e2eBackendSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= e2eBackendMaxFilesScanned {
			return filepath.SkipAll
		}
		name := d.Name()
		if !e2eBackendScanExts[strings.ToLower(filepath.Ext(name))] && !strings.HasPrefix(name, ".env") {
			return nil
		}
		// A test artifact's localhost URL is a MOCK, not the application's configuration — and by
		// the time this runs, the artifacts this run generated are already on disk. Reading them
		// back is a feedback loop: run api-b59d58ba67f66bee9364c050f4c319cc reported
		// http://localhost:3000 as an origin the app calls, a string that appears nowhere in the
		// repository, so the only place it can have come from is generated output. Warning the
		// model to intercept traffic that does not exist is worse than not warning it at all.
		if looksLikeTestArtifact(p) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > e2eBackendMaxFileBytes {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		scanned++
		for _, m := range localOriginRE.FindAllStringSubmatch(string(b), -1) {
			port, perr := strconv.Atoi(m[1])
			if perr != nil || port == appPort {
				continue
			}
			found[m[0]] = true
		}
		return nil
	})
	if len(found) == 0 {
		return nil
	}
	out := make([]string, 0, len(found))
	for o := range found {
		out = append(out, o)
	}
	sort.Strings(out)
	if len(out) > e2eBackendMaxOriginsShown {
		out = out[:e2eBackendMaxOriginsShown]
	}
	return out
}

// testArtifactMarkers name a file whose contents describe a TEST rather than the application.
var testArtifactMarkers = []string{
	".spec.", ".test.", ".cy.", ".stories.", "__tests__/", "__mocks__/", "/mocks/", "/fixtures/",
}

func looksLikeTestArtifact(p string) bool {
	low := strings.ToLower(filepath.ToSlash(p))
	for _, m := range testArtifactMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// devServerProxyFiles are the conventional names for a dev-server proxy table. Their presence means
// the app's API calls leave on the app's own port, which is served.
var devServerProxyFiles = []string{
	"proxy.conf.json", "proxy.conf.js", "proxy.conf.mjs", "proxy.config.json",
}

func hasDevServerProxy(root string) bool {
	for _, n := range devServerProxyFiles {
		if _, err := os.Stat(filepath.Join(root, n)); err == nil {
			return true
		}
	}
	return false
}

// e2eBackendBlock renders the unserved-origin warning for an E2E generation prompt, or "" when
// every origin the app uses is served.
//
// It exists because no amount of selector knowledge can rescue an assertion against a UI that never
// receives data. A spec that intercepts the call decides what the screen shows and can then assert
// on it; a spec that does not is asserting on an error state it never described.
func (g *LLMGenerator) e2eBackendNotice(ctx context.Context, lang string, isE2E bool) string {
	if !isE2E || !isJSTSLangForSelectors(lang) || strings.TrimSpace(g.RepoPath) == "" {
		return ""
	}
	g.e2eBackendOnce.Do(func() {
		origins := unservedAPIOrigins(g.RepoPath, g.E2EAppPort)
		g.e2eBackendBlock = renderE2EBackendNotice(origins, strings.TrimSpace(g.E2ESupportModule))
		g.auditE2EBackendNotice(ctx, origins)
	})
	return g.e2eBackendBlock
}

func renderE2EBackendNotice(origins []string, supportModule string) string {
	if len(origins) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n**No backend is running for these tests.** The web server started for this suite serves the ")
	b.WriteString("application only. This repository configures the application to call ")
	b.WriteString(strings.Join(origins, ", "))
	b.WriteString(", and nothing will be listening there while the suite runs, so every such request fails immediately.\n")
	b.WriteString("Intercept those calls and decide what the screen shows, registered BEFORE the navigation that triggers ")
	b.WriteString("them. Without an interception the list stays empty, any spinner is gone within a tick, and an assertion ")
	b.WriteString("on either can only fail — so assert on data-driven UI only in tests that supply the data.\n")
	if supportModule != "" {
		b.WriteString("Helpers for this are already in the repository at `" + supportModule + "`: ")
		b.WriteString("`stubJson(page, urlPattern, body, status = 200)`, `stubJsonAfter(page, urlPattern, body, delayMs, status = 200)` for a state that only ")
		b.WriteString("exists while a request is in flight, and `stubError(page, urlPattern, status = 500, body?)`. Import and use them ")
		b.WriteString("rather than writing the route handler out by hand. ")
		// The exact import line, because the model has to compute a relative specifier from a
		// directory it is told about only indirectly. In run 2026-09-03 it wrote `'../../support/api'`
		// from e2e/routes/ — one level too many — and the whole first E2E round went to
		// `Cannot find module`; it also spent a tool call on get_symbol("e2e.support.api"), which
		// the index cannot answer because the helpers are not indexed symbols.
		b.WriteString("From a spec under `" + playwrightRouteSpecDir + "/` the import is exactly ")
		b.WriteString("`import { stubJson, stubJsonAfter, stubError } from '" + supportModuleImportSpecifier(playwrightRouteSpecDir, supportModule) + "';`")
		b.WriteString(" (a spec directly under `" + filepath.ToSlash(filepath.Dir(filepath.Dir(supportModule))) + "/` uses `'" + supportModuleImportSpecifier(filepath.ToSlash(filepath.Dir(filepath.Dir(supportModule))), supportModule) + "'`). ")
		b.WriteString("The helper module is not an indexed symbol: do not look it up with `get_symbol`; the signatures above are complete.\n")
	} else {
		b.WriteString("For example: `await page.route('**/api/catalog*', route => route.fulfill({ json: [ /* rows */ ] }));`\n")
	}
	// The pattern trap, with its symptom, because the symptom points at the wrong culprit.
	b.WriteString("**Choosing the URL pattern:** match the API path, not a fragment the page route also shares. ")
	b.WriteString("`'**/catalog*'` matches `/api/catalog?q=x` AND the page route `/catalog`, so Playwright answers the ")
	b.WriteString("NAVIGATION with JSON, the application never loads, and every locator then reports \"element not found\" — ")
	b.WriteString("a failure that reads like a broken app rather than a broken pattern. Include the API segment: ")
	b.WriteString("`'**/api/catalog*'`.\n")
	return b.String()
}

func (g *LLMGenerator) auditE2EBackendNotice(ctx context.Context, origins []string) {
	if g.Audit == nil {
		return
	}
	payload := map[string]interface{}{
		"origins":        origins,
		"app_port":       g.E2EAppPort,
		"rendered":       len(origins) > 0,
		"repo_path":      g.RepoPath,
		"support_module": g.E2ESupportModule,
	}
	if len(origins) == 0 {
		payload["message"] = "No unserved API origin found; the E2E generator is not warned about a missing backend."
	} else {
		payload["message"] = fmt.Sprintf(
			"Application calls %d local origin(s) nothing serves during E2E (%s); the generator is told to intercept them.",
			len(origins), strings.Join(origins, ", "))
	}
	g.Audit.Log(ctx, "generate.e2e_backend_notice", payload)
}

// playwrightRouteSpecDir is where suggestedE2EPathForPageRouteGap places generated Playwright route
// specs; the support-module import in the E2E notice is computed relative to it.
const playwrightRouteSpecDir = "e2e/routes"

// supportModuleImportSpecifier returns the module specifier a file in fromDir uses to import
// supportModule (both repo-relative): extension stripped, always relative (`./` or `../`).
func supportModuleImportSpecifier(fromDir, supportModule string) string {
	rel, err := filepath.Rel(filepath.FromSlash(fromDir), filepath.FromSlash(supportModule))
	if err != nil {
		rel = supportModule
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
}
