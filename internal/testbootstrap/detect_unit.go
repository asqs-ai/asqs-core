package testbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DetectUnit reports whether a **unit** test stack is present (Jest, Vitest, JUnit, xUnit).
// For JS/TS, Playwright/Cypress-only setups do **not** count as a unit framework so Jest bootstrap can still run.
func DetectUnit(repoPath, lang string) (Report, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	dir := filepath.Clean(repoPath)
	switch lang {
	case "javascript", "typescript", "js", "ts":
		return detectUnitJS(dir)
	case "java":
		return detectJava(dir)
	case "csharp", "cs":
		return detectCSharp(dir)
	default:
		return Report{HasFramework: true, Reason: "bootstrap not applicable for language " + lang}, nil
	}
}

func detectUnitJS(dir string) (Report, error) {
	pkgPath := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return Report{}, err
	}
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return Report{}, err
	}
	raw := string(data)
	// Unit-oriented config files only (exclude E2E runners).
	for _, name := range []string{
		"jest.config.js", "jest.config.ts", "jest.config.mjs", "jest.config.cjs",
		"vitest.config.js", "vitest.config.ts", "vitest.config.mjs",
		"karma.conf.js", "karma.conf.ts",
		"jasmine.json",
		".mocharc.js", ".mocharc.json", ".mocharc.yaml", ".mocharc.yml",
	} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return Report{HasFramework: true, Framework: guessFrameworkFromConfig(name), Reason: "found " + name}, nil
		}
	}
	if dep := jsUnitDepFramework(raw); dep != "" {
		return Report{HasFramework: true, Framework: dep, Reason: "devDependency " + dep}, nil
	}
	if fw := jsUnitTestScriptFramework(raw); fw != "" {
		return Report{HasFramework: true, Framework: fw, Reason: "scripts.test references " + fw}, nil
	}
	return Report{HasFramework: false, Reason: "no unit test runner deps, scripts, or config found"}, nil
}

func jsUnitDepFramework(packageJSON string) string {
	var root struct {
		Dependencies    map[string]interface{} `json:"dependencies"`
		DevDependencies map[string]interface{} `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(packageJSON), &root); err != nil {
		return ""
	}
	has := func(name string) bool {
		if root.Dependencies != nil {
			if _, ok := root.Dependencies[name]; ok {
				return true
			}
		}
		if root.DevDependencies != nil {
			if _, ok := root.DevDependencies[name]; ok {
				return true
			}
		}
		return false
	}
	switch {
	case has("jest") || has("@jest/core"):
		return "jest"
	case has("vitest") || has("@vitest/runner"):
		return "vitest"
	case has("jasmine-core") || has("jasmine"):
		return "jasmine"
	case has("mocha"):
		return "mocha"
	case has("ava"):
		return "ava"
	default:
		return ""
	}
}

func jsUnitTestScriptFramework(packageJSON string) string {
	var root struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(packageJSON), &root); err != nil {
		return ""
	}
	script := strings.ToLower(strings.TrimSpace(root.Scripts["test"]))
	if script == "" {
		return ""
	}
	// Prefer unit runners first; if only playwright/cypress, no unit framework.
	subs := []struct {
		sub, fw string
	}{
		{"jest", "jest"},
		{"vitest", "vitest"},
		{"mocha", "mocha"},
		{"jasmine", "jasmine"},
		{"ava", "ava"},
		{"ng test", "angular"},
		{"nx test", "nx"},
		{"karma", "karma"},
	}
	for _, s := range subs {
		if strings.Contains(script, s.sub) {
			return s.fw
		}
	}
	return ""
}
