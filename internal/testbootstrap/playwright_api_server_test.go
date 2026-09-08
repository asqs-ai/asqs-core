package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A NestJS API has no dev server and no `--port` flag: it is started by its own `start` script
// and listens on the port its entry point names. Run api-a716cb7b25db5a880b4675a04b0b08c3 rendered
// no webServer and no baseURL for exactly this shape, so every generated
// `request.get('/health')` failed with "apiRequestContext.get: Invalid URL" before the
// application was ever exercised, and the whole run ended unstable.
func TestPlaywrightConfig_nestUsesStartScriptAndEntryPointPort(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"n","scripts":{"build":"nest build","start":"node dist/main.js"},
"dependencies":{"@nestjs/core":"^10.4.15"}}`,
		"src/main.ts": "async function bootstrap() {\n  const app = await NestFactory.create(AppModule);\n" +
			"  const port = Number(process.env.PORT ?? 3005);\n  await app.listen(port);\n}\nvoid bootstrap();\n",
	})

	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"baseURL: 'http://localhost:3005'",
		"webServer: {",
		"command: 'npm run start'",
		"port: 3005,",
		"env: { PORT: '3005' }",
		"stdout: 'pipe'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--port") {
		t.Errorf("an API server takes no --port flag:\n%s", got)
	}
	// Readiness is the open port, not a URL: an API root routinely answers 404, which Playwright's
	// url probe never accepts, so a url probe would time out against a server that is up.
	if strings.Contains(got, "url: 'http://localhost:3005'") {
		t.Errorf("readiness must be the port, not a url probe:\n%s", got)
	}
}

// NestJS's own template listens on 3000 and reads nothing from the environment; a Nest package
// whose entry point names no port still gets that default rather than no server at all.
func TestPlaywrightConfig_nestDefaultsTo3000WithoutPortEvidence(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"n","scripts":{"start":"nest start"},"dependencies":{"@nestjs/core":"^10.4.15"}}`,
	})
	if err := writePlaywrightConfig(dir, "typescript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	got := string(b)
	if !strings.Contains(got, "port: 3000,") || !strings.Contains(got, "baseURL: 'http://localhost:3000'") {
		t.Errorf("want the Nest default port 3000:\n%s", got)
	}
}

// A plain Express service is served the same way when its entry point names the port.
func TestPlaywrightConfig_expressReadsListenPortFromEntry(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"x","main":"server.js","scripts":{"start":"node server.js"},"dependencies":{"express":"^4.19.2"}}`,
		"server.js":    "const app = require('express')();\napp.listen(process.env.PORT || 4010, () => {});\n",
	})
	if err := writePlaywrightConfig(dir, "javascript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	got := string(b)
	if !strings.Contains(got, "port: 4010,") || !strings.Contains(got, "command: 'npm run start'") {
		t.Errorf("want the express server on 4010:\n%s", got)
	}
}

// Without a framework that implies a port and without port evidence in the entry point, nothing is
// emitted: a guessed port would hang every E2E run until webServer.timeout expires.
func TestPlaywrightConfig_expressWithoutPortEvidenceStaysUndecidable(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"x","scripts":{"start":"node server.js"},"dependencies":{"express":"^4.19.2"}}`,
		"server.js":    "const app = require('express')();\napp.listen(config.port);\n",
	})
	if err := writePlaywrightConfig(dir, "javascript"); err != nil {
		t.Fatalf("writePlaywrightConfig: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "playwright.config.ts"))
	if strings.Contains(string(b), "webServer") {
		t.Errorf("emitted a webServer with an unknown port:\n%s", b)
	}
}

func TestParseListenPort(t *testing.T) {
	cases := map[string]int{
		"await app.listen(port);\nconst port = Number(process.env.PORT ?? 3000);": 3000,
		"app.listen(process.env.PORT || 8081, () => {})":                           8081,
		"app.listen(+process.env['PORT'] || 5000)":                                 5000,
		"server.listen(4040);":                                                     4040,
		"await app.listen({ port: 7070, host: '0.0.0.0' });":                       7070,
		"app.listen(config.port);":                                                 0,
		"":                                                                         0,
		// A port outside the valid range is not a port.
		"app.listen(99999);": 0,
	}
	for src, want := range cases {
		if got := parseListenPort(src); got != want {
			t.Errorf("parseListenPort(%q) = %d, want %d", src, got, want)
		}
	}
}

// The port the generator is told about must agree with the one written into the config, because
// the unserved-backend notice excludes exactly that origin.
func TestE2EAppPort_reportsAPIServerPort(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"package.json": `{"name":"n","scripts":{"start":"node dist/main.js"},"dependencies":{"@nestjs/core":"^10.4.15"}}`,
		"src/main.ts":  "await app.listen(3100);\n",
	})
	if got := E2EAppPort(dir, "typescript"); got != 3100 {
		t.Errorf("E2EAppPort = %d, want 3100", got)
	}
}
