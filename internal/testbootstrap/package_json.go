package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mergeJSTestDepsIntoPackageJSON merges the profile's missing devDependencies and adds its runner
// script.
//
// Two changes from the previous behaviour, both of which were destructive:
//
//   - the dependency set now comes from the profile rather than a fixed jest/ts-jest list, so a React
//     package gets jsdom and Testing Library and an Angular package gets its preset;
//   - the script is written to `test:asqs`, NOT `test`. The old code did `scripts.test = "jest"`
//     unconditionally, which destroyed `ng test` on Angular repos and any custom harness elsewhere.
//     `test` is only set when the package has no test script at all.
func mergeJSTestDepsIntoPackageJSON(pkgPath string, p jsTestProfile, missing []jsDep, pinVersions bool) (addedScript string, err error) {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return "", err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("package.json: %w", err)
	}
	dev, _ := root["devDependencies"].(map[string]interface{})
	if dev == nil {
		dev = make(map[string]interface{})
		root["devDependencies"] = dev
	}
	ver := func(v string) interface{} {
		if pinVersions {
			return v
		}
		return "^" + v
	}
	for _, d := range missing {
		dev[d.Name] = ver(d.Version)
	}

	scripts, _ := root["scripts"].(map[string]interface{})
	if scripts == nil {
		scripts = make(map[string]interface{})
		root["scripts"] = scripts
	}
	runnerCmd := "jest"
	if p.Runner == JSRunnerVitest {
		runnerCmd = "vitest run"
	}
	scripts[p.TestScript] = runnerCmd
	addedScript = p.TestScript
	if existing, ok := scripts["test"].(string); !ok || strings.TrimSpace(existing) == "" {
		scripts["test"] = runnerCmd
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	return addedScript, atomicWrite(pkgPath, out)
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".asqs-bootstrap-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
