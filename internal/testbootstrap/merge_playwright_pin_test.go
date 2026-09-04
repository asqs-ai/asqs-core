package testbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// pin_versions=false must not turn the Playwright pin into a caret range: the E2E image ships the
// browsers of exactly one release (see mergePlaywrightIntoPackageJSON).
func TestMergePlaywrightIntoPackageJSON_exactVersionEvenWhenNotPinning(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkgPath, []byte(`{"name":"demo","devDependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergePlaywrightIntoPackageJSON(pkgPath, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Dev map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if got := root.Dev["@playwright/test"]; got != VersionPlaywrightTest {
		t.Fatalf("@playwright/test = %q, want the exact %q", got, VersionPlaywrightTest)
	}
}
