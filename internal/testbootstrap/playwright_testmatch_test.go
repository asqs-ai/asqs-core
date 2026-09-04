package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTMFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The asqs-core run of 2026-09-03 generated e2e/smoke.e2e-spec.ts as an E2E artifact; Playwright's
// default testMatch never loaded it, so it would have shipped unexecuted.
func TestPlaywrightConfig_addsE2ESpecSuffixWhenTheRepositoryUsesIt(t *testing.T) {
	dir := t.TempDir()
	writeTMFile(t, dir, "e2e/smoke.e2e-spec.ts", "import { test, expect } from '@playwright/test';\n\ntest('smoke: shell loads', async ({ page }) => {\n  await page.goto('/');\n});\n")
	extra := extraPlaywrightTestMatch(dir)
	if len(extra) != 1 || extra[0] != playwrightE2ESpecSuffixTestMatch {
		t.Fatalf("extra = %v, want [%s]", extra, playwrightE2ESpecSuffixTestMatch)
	}
	got := renderPlaywrightConfig(playwrightWebServer{}, extra)
	want := "  testMatch: ['" + playwrightDefaultTestMatch + "', '" + playwrightE2ESpecSuffixTestMatch + "'],\n"
	if !strings.Contains(got, want) {
		t.Fatalf("config lacks the combined testMatch:\n%s", got)
	}
	if !strings.Contains(got, "testDir: './e2e'") {
		t.Fatal("testDir must survive")
	}
}

func TestPlaywrightConfig_noTestMatchWithoutE2ESpecFiles(t *testing.T) {
	dir := t.TempDir()
	writeTMFile(t, dir, "e2e/smoke.spec.ts", "import { test } from '@playwright/test';\ntest('x', async () => {});\n")
	if extra := extraPlaywrightTestMatch(dir); extra != nil {
		t.Fatalf("extra = %v, want none: the default pattern already covers *.spec.ts", extra)
	}
	if got := renderPlaywrightConfig(playwrightWebServer{}, nil); strings.Contains(got, "testMatch") {
		t.Fatalf("no testMatch expected without extra patterns:\n%s", got)
	}
	if extra := extraPlaywrightTestMatch(t.TempDir()); extra != nil {
		t.Fatalf("extra = %v for a repo without e2e/", extra)
	}
}

// Protractor is where the suffix comes from; its specs use `browser`/`element` globals Playwright
// does not provide, and loading one would fail the whole run at import.
func TestPlaywrightConfig_skipsE2ESpecSuffixForProtractorSpecs(t *testing.T) {
	dir := t.TempDir()
	writeTMFile(t, dir, "e2e/src/app.e2e-spec.ts", "import { browser, logging } from 'protractor';\nimport { AppPage } from './app.po';\n\ndescribe('workspace-project App', () => {});\n")
	writeTMFile(t, dir, "e2e/other.e2e-spec.ts", "test('x', () => {});\n")
	if extra := extraPlaywrightTestMatch(dir); extra != nil {
		t.Fatalf("extra = %v, want none when a Protractor spec is present", extra)
	}
}

// A bare `test(` with no import is what the fixture ships; matching it at bootstrap made
// `playwright test --list` fail with `ReferenceError: test is not defined` (run of 2026-09-04).
func TestPlaywrightConfig_ignoresE2ESpecFilesWithoutThePlaywrightImport(t *testing.T) {
	dir := t.TempDir()
	writeTMFile(t, dir, "e2e/smoke.e2e-spec.ts", "// marker\ntest('smoke: shell loads', () => {\n  void 0;\n});\n")
	if extra := extraPlaywrightTestMatch(dir); extra != nil {
		t.Fatalf("extra = %v, want none for a file that does not import @playwright/test", extra)
	}
}

func TestEnsurePlaywrightImport_addsTheImportOnce(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "smoke.e2e-spec.ts")
	writeTMFile(t, dir, "smoke.e2e-spec.ts", "// Simulated marker\ntest('smoke: shell loads', () => {\n  void 0;\n});\n")
	wrote, err := EnsurePlaywrightImport(spec)
	if err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v, want a write", wrote, err)
	}
	b, _ := os.ReadFile(spec)
	got := string(b)
	if !strings.HasPrefix(got, "// Simulated marker\nimport { test, expect } from '@playwright/test';\ntest(") {
		t.Fatalf("import not inserted after the opening comment:\n%s", got)
	}
	if wrote, _ = EnsurePlaywrightImport(spec); wrote {
		t.Fatal("second call must be a no-op")
	}
	// Files that already import, call no test API, or are Protractor specs are left alone.
	for name, body := range map[string]string{
		"has.spec.ts":       "import { test } from '@playwright/test';\ntest('x', async () => {});\n",
		"helper.ts":         "export const x = 1;\n",
		"protractor.e2e.ts": "import { browser } from 'protractor';\ndescribe('p', () => { it('x', () => { expect(1).toBe(1); }); });\n",
	} {
		writeTMFile(t, dir, name, body)
		if wrote, _ := EnsurePlaywrightImport(filepath.Join(dir, name)); wrote {
			t.Errorf("%s: must not be rewritten", name)
		}
	}
}

// After generation extends the spec with the import, the owned config must pick the suffix up;
// a repository's own config is never rewritten and no config is created from nothing.
func TestRefreshPlaywrightConfig_rerendersOwnedConfigOnly(t *testing.T) {
	dir := t.TempDir()
	writeTMFile(t, dir, "package.json", `{"name":"x","scripts":{"start":"ng serve"},"devDependencies":{"@angular/core":"^19.2.0","@playwright/test":"1.49.1"}}`)
	writeTMFile(t, dir, "e2e/smoke.e2e-spec.ts", "test('smoke', () => {});\n")
	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts")); strings.Contains(string(b), "testMatch") {
		t.Fatal("bootstrap config must not match a spec without the import")
	}
	if _, err := EnsurePlaywrightImport(filepath.Join(dir, "e2e", "smoke.e2e-spec.ts")); err != nil {
		t.Fatal(err)
	}
	rel, err := RefreshPlaywrightConfig(dir, "typescript")
	if err != nil || rel != "playwright.config.ts" {
		t.Fatalf("refresh rel=%q err=%v, want playwright.config.ts rewritten", rel, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts")); !strings.Contains(string(b), playwrightE2ESpecSuffixTestMatch) {
		t.Fatalf("refreshed config lacks the suffix pattern:\n%s", b)
	}
	if rel, _ := RefreshPlaywrightConfig(dir, "typescript"); rel != "" {
		t.Errorf("unchanged content must not be rewritten again; got %q", rel)
	}
	// User-owned config: untouched.
	own := t.TempDir()
	writeTMFile(t, own, "package.json", `{"name":"y"}`)
	writeTMFile(t, own, "playwright.config.ts", "export default {};\n")
	if rel, _ := RefreshPlaywrightConfig(own, "typescript"); rel != "" {
		t.Errorf("user-owned config rewritten: %q", rel)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "playwright.config.ts")); string(b) != "export default {};\n" {
		t.Error("user-owned config content changed")
	}
	// No config at all: nothing created.
	none := t.TempDir()
	writeTMFile(t, none, "package.json", `{"name":"z"}`)
	if rel, _ := RefreshPlaywrightConfig(none, "typescript"); rel != "" {
		t.Errorf("config created from nothing: %q", rel)
	}
	if _, err := os.Stat(filepath.Join(none, "playwright.config.ts")); err == nil {
		t.Error("playwright.config.ts must not be created by a refresh")
	}
}
