package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
)

// mergePlaywrightIntoPackageJSON adds @playwright/test and scripts.test:e2e without replacing scripts.test (unit).
func mergePlaywrightIntoPackageJSON(pkgPath string, pinVersions bool) error {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("package.json: %w", err)
	}
	dev, _ := root["devDependencies"].(map[string]interface{})
	if dev == nil {
		dev = make(map[string]interface{})
		root["devDependencies"] = dev
	}
	// Always the exact version, whatever pin_versions says. The E2E pass runs in the Playwright
	// image whose browsers are built for exactly one @playwright/test release, and a caret range
	// lets npm move off it: the asqs-core run of 2026-09-03 wrote `^1.49.1`, resolved a release
	// wanting chromium_headless_shell-1234, and no browser could ever have been found. The runner
	// also derives the image tag from the installed version (runner.InstalledPlaywrightTestVersion),
	// so this pin keeps the bootstrap and the image in step rather than being the only defence.
	_ = pinVersions
	dev["@playwright/test"] = VersionPlaywrightTest
	scripts, _ := root["scripts"].(map[string]interface{})
	if scripts == nil {
		scripts = make(map[string]interface{})
		root["scripts"] = scripts
	}
	if _, ok := scripts["test:e2e"]; !ok {
		scripts["test:e2e"] = "playwright test"
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return atomicWrite(pkgPath, out)
}
