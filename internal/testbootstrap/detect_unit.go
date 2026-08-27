package testbootstrap

import (
	"encoding/json"
	"fmt"
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

// detectUnitJS reports whether the package already carries the COMPLETE stack its framework needs.
//
// Two changes from the previous rule:
//
//   - it resolves the package the way bootstrap does (walking to a nested package.json when the repo
//     root has none) instead of stat-ing the repo root and erroring out. A monorepo without a root
//     package.json used to fail the whole bootstrap with "no such file or directory";
//   - "jest is in devDependencies" is no longer sufficient. A React package with plain Jest has no
//     jsdom environment and no Testing Library, so every generated component test fails on
//     `document is not defined` — a failure that lives in package.json and the runner config, neither
//     of which the fix loop may write.
func detectUnitJS(dir string) (Report, error) {
	pkgDir, err := resolveJSPackageDirForBootstrap(dir)
	if err != nil {
		return Report{}, err
	}
	dir = pkgDir
	pkgPath := filepath.Join(dir, "package.json")
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
	// A runner is present. Ask the framework-aware question: is the stack COMPLETE?
	runnerName := jsUnitDepFramework(raw)
	if runnerName == "" {
		runnerName = jsUnitTestScriptFramework(raw)
	}

	det, derr := detectJSFramework(dir, "")
	if derr != nil {
		if runnerName != "" {
			return Report{HasFramework: true, Framework: runnerName, Reason: "devDependency " + runnerName}, nil
		}
		return Report{HasFramework: false, Reason: "no unit test runner deps, scripts, or config found"}, nil
	}
	prof := buildJSTestProfile(det)
	if prof.Declined {
		return Report{HasFramework: true, Framework: string(prof.Framework), Reason: prof.DeclinedReason}, nil
	}
	pkg, perr := readJSPackageJSON(dir)
	if perr != nil {
		return Report{}, perr
	}
	missing := prof.missingDeps(pkg)
	if len(missing) == 0 && runnerName != "" {
		return Report{
			HasFramework: true,
			Framework:    prof.Stack,
			Reason: fmt.Sprintf("package.json already carries the full %s (%s) stack: %s",
				prof.Framework, prof.Stack, strings.Join(describeJSDeps(prof.Deps), ", ")),
		}, nil
	}
	if runnerName == "" && len(missing) == len(prof.Deps) {
		return Report{HasFramework: false, Framework: prof.Stack, Reason: fmt.Sprintf(
			"%s package with no unit test runner; %s will be installed", prof.Framework, strings.Join(describeJSDeps(prof.Deps), ", "))}, nil
	}
	return Report{HasFramework: false, Framework: prof.Stack, Reason: fmt.Sprintf(
		"%s package missing %s", prof.Framework, strings.Join(describeJSDeps(missing), ", "))}, nil
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
