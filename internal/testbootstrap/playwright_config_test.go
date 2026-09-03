package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePlaywrightSmokeSpec_createsFile(t *testing.T) {
	dir := t.TempDir()
	if err := writePlaywrightSmokeSpec(dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "e2e", "smoke.spec.ts")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if len(b) < 50 || !strings.Contains(s, "@playwright/test") || !strings.Contains(s, "bootstrap smoke") {
		t.Fatalf("unexpected smoke spec: %s", s)
	}
}

func TestWritePlaywrightSmokeSpec_idempotent(t *testing.T) {
	dir := t.TempDir()
	custom := "// keep\n"
	if err := os.MkdirAll(filepath.Join(dir, "e2e"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "e2e", "smoke.spec.ts"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writePlaywrightSmokeSpec(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "e2e", "smoke.spec.ts"))
	if string(b) != custom {
		t.Fatalf("existing smoke.spec.ts should not be overwritten; got %q", string(b))
	}
}

func writePkg(t *testing.T, files map[string]string) string {
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

const angularPkgJSON = `{"name":"a","scripts":{"start":"ng serve","build":"ng build"},
"dependencies":{"@angular/core":"^19.2.0"}}`

// Without baseURL a generated spec's `page.goto('/checkout')` fails with "Cannot navigate to
// invalid URL" before the app is exercised at all, and the fixer cannot repair it because
// playwright.config.ts is not a writable artifact. Run api-874025913eb1599cccc74ae04a5ac46b lost
// six of seven E2E tests to exactly that.
func TestPlaywrightConfig_angularGetsBaseURLAndWebServer(t *testing.T) {
	dir := writePkg(t, map[string]string{"package.json": angularPkgJSON})

	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, want := range []string{
		"baseURL: 'http://localhost:4200'",
		"webServer: {",
		"command: 'npm run start -- --port 4200'",
		"url: 'http://localhost:4200'",
		"timeout: 120_000",
		"stdout: 'pipe'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
}

// Vite owns the port for whatever framework it serves, and its templates name the script "dev".
func TestPlaywrightConfig_viteReactUsesDevScriptAndVitePort(t *testing.T) {
	// detectJSFramework only reports ViteMajor when a vite config is actually on disk, which is
	// the signal we want: the dependency alone does not mean Vite serves this app.
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"r","scripts":{"dev":"vite","build":"vite build"},
"dependencies":{"react":"^18.3.1"},"devDependencies":{"vite":"^6.0.0"}}`,
		"vite.config.ts": "export default {};\n",
	})

	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	got := string(b)
	if !strings.Contains(got, "command: 'npm run dev -- --port 5173'") {
		t.Errorf("want the vite dev script on 5173:\n%s", got)
	}
	if !strings.Contains(got, "baseURL: 'http://localhost:5173'") {
		t.Errorf("want the vite baseURL:\n%s", got)
	}
}

// A wrong webServer command hangs every E2E run until webServer.timeout, which is worse than the
// navigation failure it would replace. When either half is unknown, emit neither half.
func TestPlaywrightConfig_omitsWebServerWhenUndecidable(t *testing.T) {
	cases := map[string]string{
		"no dev-server script": `{"name":"a","scripts":{"build":"ng build"},"dependencies":{"@angular/core":"^19.2.0"}}`,
		"no known port":        `{"name":"p","scripts":{"start":"node server.js"}}`,
	}
	for name, pkg := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writePkg(t, map[string]string{"package.json": pkg})
			if err := writePlaywrightConfig(dir, "typescript"); err != nil {
				t.Fatalf("writePlaywrightConfig: %v", err)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
			got := string(b)
			if strings.Contains(got, "webServer") {
				t.Errorf("emitted a webServer it cannot name:\n%s", got)
			}
			if strings.Contains(got, "baseURL") {
				t.Errorf("emitted a baseURL with nothing serving it:\n%s", got)
			}
			// Still a valid config: testDir and the chromium project must survive.
			if !strings.Contains(got, "testDir: './e2e'") || !strings.Contains(got, "chromium") {
				t.Errorf("degraded config lost its non-server settings:\n%s", got)
			}
		})
	}
}

// Generated tests ship, so an ASQS-written config from an earlier run is on disk for the next one.
// The old bare existence check pinned every repository to the first version ASQS ever wrote.
func TestPlaywrightConfig_upgradesOwnedConfigButNeverTheRepositorys(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json":         angularPkgJSON,
		"playwright.config.ts": "// " + asqsE2EGeneratedHeader + " — safe to edit or delete.\n// stale\n",
	})
	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	if !strings.Contains(string(b), "webServer") {
		t.Errorf("an ASQS-owned config was not upgraded:\n%s", b)
	}

	const mine = "// my own playwright config\n"
	dir2 := writePkg(t, map[string]string{
		"package.json":         angularPkgJSON,
		"playwright.config.ts": mine,
	})
	if err := writePlaywrightConfig(dir2, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir2, "playwright.config.ts"))
	if string(b2) != mine {
		t.Errorf("clobbered a repository's own config:\n%s", b2)
	}
}

// The unit bootstrap's tsconfig.spec.json and its jest config must agree about which files are
// tests: jsTestMatchGlobs accepts spec AND test, and layout emits *.test.ts on every jest path, so
// an include listing only *.spec.ts covered none of the generated tests.
func TestAngularTsconfigSpec_includesBothTestSuffixes(t *testing.T) {
	for _, want := range []string{`src/**/*.spec.ts`, `src/**/*.test.ts`} {
		if !strings.Contains(angularTsconfigSpec, want) {
			t.Errorf("angularTsconfigSpec does not include %q:\n%s", want, angularTsconfigSpec)
		}
	}
}

// Angular's generated tsconfig sets noPropertyAccessFromIndexSignature, under which
// `process.env.CI` is TS4111. Run api-7e4930f7306db0a480d4ced6c4107ede reported it four times
// against this generated file once the post-generate type-check gate was switched on — the config
// ASQS writes must itself survive the gate ASQS runs.
func TestPlaywrightConfig_usesBracketAccessForEnv(t *testing.T) {
	dir := writePkg(t, map[string]string{"package.json": angularPkgJSON})
	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	got := string(b)

	if strings.Contains(got, "process.env.CI") {
		t.Errorf("dotted env access is TS4111 under noPropertyAccessFromIndexSignature:\n%s", got)
	}
	if !strings.Contains(got, "process.env['CI']") {
		t.Errorf("want bracket access:\n%s", got)
	}
	// Every env read must be bracketed, in the webServer block too.
	if n := strings.Count(got, "process.env['CI']"); n != 4 {
		t.Errorf("process.env['CI'] appears %d times, want 4 (forbidOnly, retries, workers, reuseExistingServer)", n)
	}
}
