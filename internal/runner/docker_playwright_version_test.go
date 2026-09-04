package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writePWFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The asqs-core run of 2026-09-03 installed an @playwright/test that wanted
// chromium_headless_shell-1234 while the image was pinned for 1.49.1. The image tag must follow what
// npm actually resolved, read from node_modules first.
func TestInstalledPlaywrightTestVersion_prefersNodeModules(t *testing.T) {
	dir := t.TempDir()
	writePWFile(t, dir, "node_modules/@playwright/test/package.json", `{"name":"@playwright/test","version":"1.56.2"}`)
	writePWFile(t, dir, "package-lock.json", `{"lockfileVersion":3,"packages":{"node_modules/@playwright/test":{"version":"1.49.1"}}}`)
	if got := InstalledPlaywrightTestVersion(dir); got != "1.56.2" {
		t.Fatalf("version = %q, want 1.56.2 from node_modules", got)
	}
}

func TestInstalledPlaywrightTestVersion_fallsBackToLockfile(t *testing.T) {
	v3 := t.TempDir()
	writePWFile(t, v3, "package-lock.json", `{"lockfileVersion":3,"packages":{"":{"name":"x"},"node_modules/@playwright/test":{"version":"1.52.0"}}}`)
	if got := InstalledPlaywrightTestVersion(v3); got != "1.52.0" {
		t.Fatalf("v3 lockfile version = %q, want 1.52.0", got)
	}
	v1 := t.TempDir()
	writePWFile(t, v1, "package-lock.json", `{"lockfileVersion":1,"dependencies":{"@playwright/test":{"version":"1.50.1"}}}`)
	if got := InstalledPlaywrightTestVersion(v1); got != "1.50.1" {
		t.Fatalf("v1 lockfile version = %q, want 1.50.1", got)
	}
}

func TestInstalledPlaywrightTestVersion_rejectsNonReleaseVersions(t *testing.T) {
	dir := t.TempDir()
	writePWFile(t, dir, "node_modules/@playwright/test/package.json", `{"version":"1.57.0-alpha-2026-01-01"}`)
	if got := InstalledPlaywrightTestVersion(dir); got != "" {
		t.Fatalf("version = %q, want \"\" for a pre-release the image registry does not tag", got)
	}
	if got := InstalledPlaywrightTestVersion(t.TempDir()); got != "" {
		t.Fatalf("version = %q, want \"\" for a directory with neither node_modules nor lockfile", got)
	}
}

func TestPlaywrightDockerImageRefFor_configuredDerivedDefault(t *testing.T) {
	derived := t.TempDir()
	writePWFile(t, derived, "node_modules/@playwright/test/package.json", `{"version":"1.56.2"}`)

	configured := &Sandbox{ImagePlaywright: "registry.example/pw:custom"}
	if got := configured.playwrightDockerImageRefFor(derived); got != "registry.example/pw:custom" {
		t.Fatalf("configured image must win; got %q", got)
	}
	s := &Sandbox{}
	if got := s.playwrightDockerImageRefFor(derived); got != "mcr.microsoft.com/playwright:v1.56.2-jammy" {
		t.Fatalf("derived image = %q, want mcr.microsoft.com/playwright:v1.56.2-jammy", got)
	}
	if got := s.playwrightDockerImageRefFor(t.TempDir()); got != DefaultPlaywrightDockerImage {
		t.Fatalf("image without any version evidence = %q, want the default %s", got, DefaultPlaywrightDockerImage)
	}
	if got := PlaywrightDockerImageForVersion("1.49.1"); got != DefaultPlaywrightDockerImage {
		t.Fatalf("the default image must be the pinned bootstrap version's image; got %q", got)
	}
}
