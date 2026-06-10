package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// mergeJestIntoPackageJSON reads package.json, merges devDependencies and always sets scripts.test to "jest".
func mergeJestIntoPackageJSON(pkgPath string, isTS, pinVersions bool) error {
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
	dev["jest"] = ver(VersionJest)
	if isTS {
		dev["ts-jest"] = ver(VersionTSJest)
		dev["@types/jest"] = ver(VersionTypesJest)
		dev["@types/node"] = ver(VersionTypesNode)
	}
	scripts, _ := root["scripts"].(map[string]interface{})
	if scripts == nil {
		scripts = make(map[string]interface{})
		root["scripts"] = scripts
	}
	scripts["test"] = "jest"
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return atomicWrite(pkgPath, out)
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
