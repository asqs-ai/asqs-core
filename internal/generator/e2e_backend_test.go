package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// THE ROOT CAUSE THIS NOTICE EXISTS FOR. Run api-620c78444155f43a6afdc9587c097eae served the app on
// 4200 while it was configured to call http://localhost:4201/api, with nothing on 4201: every
// request failed on the next tick, so the results list never populated and the loading flag was true
// for microseconds. No selector knowledge can rescue an assertion against a UI that never gets data.
func TestUnservedAPIOrigins_findsTheConfiguredBackend(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
	})
	got := unservedAPIOrigins(dir, 4200)
	if len(got) != 1 || got[0] != "http://localhost:4201" {
		t.Fatalf("origins = %v, want [http://localhost:4201]", got)
	}
}

// The app's own origin IS served, so warning about it would send the model chasing traffic that
// works.
func TestUnservedAPIOrigins_excludesTheAppsOwnPort(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/config.ts": `export const base = 'http://localhost:4200';`,
	})
	if got := unservedAPIOrigins(dir, 4200); len(got) != 0 {
		t.Fatalf("origins = %v, want none — 4200 is the served app", got)
	}
}

// A false origin is worse than a missing one: it tells the model to intercept traffic that does not
// exist. Editor and CI configuration name ports the application never calls — the fixture's
// .vscode/launch.json carries Karma's 9876.
func TestUnservedAPIOrigins_ignoresToolingAndVendorTrees(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
		".vscode/launch.json":             `{"url": "http://localhost:9876/debug"}`,
		".github/workflows/ci.yml":        `run: curl http://localhost:5555`,
		"node_modules/pkg/index.js":       `const x = 'http://localhost:7777';`,
		"dist/main.js":                    `const y = 'http://localhost:8888';`,
		"e2e/routes/catalog.spec.ts":      `await page.goto('http://localhost:9999');`,
	})
	got := unservedAPIOrigins(dir, 4200)
	if len(got) != 1 || got[0] != "http://localhost:4201" {
		t.Fatalf("origins = %v, want only the application's configured backend", got)
	}
}

// A dev-server proxy means the app's API calls leave on the app's own port, which is served. There
// is nothing to intercept and nothing to warn about.
func TestUnservedAPIOrigins_silentBehindADevServerProxy(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
		"proxy.conf.json":                 `{"/api": {"target": "http://localhost:4201"}}`,
	})
	if got := unservedAPIOrigins(dir, 4200); len(got) != 0 {
		t.Fatalf("origins = %v, want none behind a proxy", got)
	}
}

func TestE2EBackendNotice_renderingAndGates(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
	})

	g := &LLMGenerator{RepoPath: dir, E2EAppPort: 4200}
	block := g.e2eBackendNotice(context.Background(), "typescript", true)
	for _, want := range []string{"http://localhost:4201", "page.route(", "BEFORE the navigation"} {
		if !strings.Contains(block, want) {
			t.Errorf("notice missing %q:\n%s", want, block)
		}
	}

	// Gates: a unit item, a non-JS/TS language, and a repo with no unserved origin all render "".
	for name, tc := range map[string]struct {
		gen   *LLMGenerator
		lang  string
		isE2E bool
	}{
		"unit item":  {&LLMGenerator{RepoPath: dir, E2EAppPort: 4200}, "typescript", false},
		"non js/ts":  {&LLMGenerator{RepoPath: dir, E2EAppPort: 4200}, "java", true},
		"no repo":    {&LLMGenerator{E2EAppPort: 4200}, "typescript", true},
		"all served": {&LLMGenerator{RepoPath: writeRepoFiles(t, map[string]string{"src/a.ts": "const x = 1;"}), E2EAppPort: 4200}, "typescript", true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.gen.e2eBackendNotice(context.Background(), tc.lang, tc.isE2E); got != "" {
				t.Errorf("want no notice, got:\n%s", got)
			}
		})
	}
}

// Repo-wide and identical for every gap, like the selector inventory, so it must resolve once.
func TestE2EBackendNotice_resolvesOncePerGenerator(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
	})
	g := &LLMGenerator{RepoPath: dir, E2EAppPort: 4200}
	first := g.e2eBackendNotice(context.Background(), "typescript", true)
	if second := g.e2eBackendNotice(context.Background(), "typescript", true); first != second {
		t.Error("notice is not stable across gaps; the system-prompt cache breakpoint depends on it")
	}
}

// THE FEEDBACK LOOP. By the time this scan runs, the test artifacts this run generated are already
// on disk, and a mock URL inside one is not the application's configuration. Run
// api-b59d58ba67f66bee9364c050f4c319cc reported http://localhost:3000 as an origin the app calls —
// a string that appears nowhere in the repository, so it can only have come from generated output.
func TestUnservedAPIOrigins_ignoresGeneratedTestArtifacts(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts":                    `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
		"src/app/features/catalog/catalog.service.test.ts":   `jest.mock('x'); const api = 'http://localhost:3000/api';`,
		"src/app/features/catalog/catalog.component.spec.ts": `const base = 'http://localhost:3001';`,
		"src/__mocks__/api.ts":                               `export const url = 'http://localhost:3002';`,
		"src/fixtures/rows.ts":                               `export const endpoint = 'http://localhost:3003';`,
		"src/app/x.stories.ts":                               `const s = 'http://localhost:3004';`,
	})

	got := unservedAPIOrigins(dir, 4200)
	if len(got) != 1 || got[0] != "http://localhost:4201" {
		t.Fatalf("origins = %v, want only the application's own configuration", got)
	}
}

// The URL-pattern trap has a symptom that points at the wrong culprit: `**/catalog*` also matches
// the PAGE route, so Playwright answers the navigation with JSON, the app never boots, and every
// locator reports "element not found". I hit it myself while proving interception works.
func TestE2EBackendNotice_warnsAboutTheURLPatternTrap(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
	})
	g := &LLMGenerator{RepoPath: dir, E2EAppPort: 4200}
	block := g.e2eBackendNotice(context.Background(), "typescript", true)

	for _, want := range []string{"'**/catalog*'", "element not found", "'**/api/catalog*'"} {
		if !strings.Contains(block, want) {
			t.Errorf("notice missing %q:\n%s", want, block)
		}
	}
}

// Name the helpers only when they exist. A repository that brought its own E2E setup never received
// them, and telling it to import a missing module trades one broken spec for another.
func TestE2EBackendNotice_namesTheHelpersOnlyWhenPresent(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"src/environments/environment.ts": `export const environment = { apiBaseUrl: 'http://localhost:4201/api' };`,
	})

	with := &LLMGenerator{RepoPath: dir, E2EAppPort: 4200, E2ESupportModule: "e2e/support/api.ts"}
	block := with.e2eBackendNotice(context.Background(), "typescript", true)
	for _, want := range []string{"e2e/support/api.ts", "stubJson(", "stubJsonAfter(", "stubError("} {
		if !strings.Contains(block, want) {
			t.Errorf("notice missing %q:\n%s", want, block)
		}
	}

	without := &LLMGenerator{RepoPath: dir, E2EAppPort: 4200}
	plain := without.e2eBackendNotice(context.Background(), "typescript", true)
	if strings.Contains(plain, "e2e/support/api.ts") || strings.Contains(plain, "stubJson(") {
		t.Errorf("named helpers that do not exist:\n%s", plain)
	}
	// It must still say what to do, just inline.
	if !strings.Contains(plain, "page.route(") {
		t.Errorf("no interception guidance without the helpers:\n%s", plain)
	}
}
