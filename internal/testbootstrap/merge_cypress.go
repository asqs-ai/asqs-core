package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
)

// mergeCypressIntoPackageJSON adds cypress and scripts.test:e2e (cypress run).
func mergeCypressIntoPackageJSON(pkgPath string, pinVersions bool) error {
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
	ver := func(v string) interface{} {
		if pinVersions {
			return v
		}
		return "^" + v
	}
	dev["cypress"] = ver(VersionCypress)
	scripts, _ := root["scripts"].(map[string]interface{})
	if scripts == nil {
		scripts = make(map[string]interface{})
		root["scripts"] = scripts
	}
	if _, ok := scripts["test:e2e"]; !ok {
		scripts["test:e2e"] = "cypress run"
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return atomicWrite(pkgPath, out)
}
