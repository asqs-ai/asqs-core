// Package testbootstrap detects missing test frameworks and can apply minimal Jest setup for JS/TS.
// Detection rules align with tools/js-ts-indexer/src/discovery.ts where applicable.
package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Report is the outcome of Detect.
type Report struct {
	// HasFramework is true when an existing test setup is detected (no bootstrap needed).
	HasFramework bool
	// Framework is a short name when known (e.g. jest, vitest, junit).
	Framework string
	// Reason explains the detection result for logs and audit.
	Reason string
}

// Detect inspects repoPath for language lang (javascript, typescript, java, csharp, cs).
func Detect(repoPath, lang string) (Report, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	dir := filepath.Clean(repoPath)
	switch lang {
	case "javascript", "typescript", "js", "ts":
		return detectJS(dir)
	case "java":
		return detectJava(dir)
	case "csharp", "cs":
		return detectCSharp(dir)
	default:
		return Report{HasFramework: true, Reason: "bootstrap not applicable for language " + lang}, nil
	}
}

func detectJS(dir string) (Report, error) {
	roots, err := jsPackageRootsForDetection(dir)
	if err != nil {
		return Report{}, err
	}
	if len(roots) == 0 {
		return Report{}, fmt.Errorf("no package.json under repo")
	}
	var last Report
	for _, root := range roots {
		rep, err := detectJSInPackageRoot(root)
		if err != nil {
			return Report{}, err
		}
		if rep.HasFramework {
			return rep, nil
		}
		last = rep
	}
	return last, nil
}

func detectJSInPackageRoot(dir string) (Report, error) {
	pkgPath := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return Report{}, err
	}
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return Report{}, err
	}
	raw := string(data)
	for _, name := range []string{
		"jest.config.js", "jest.config.ts", "jest.config.mjs", "jest.config.cjs",
		"vitest.config.js", "vitest.config.ts", "vitest.config.mjs",
		"playwright.config.js", "playwright.config.ts",
		"cypress.config.js", "cypress.config.ts",
		"karma.conf.js", "karma.conf.ts",
		"jasmine.json",
		".mocharc.js", ".mocharc.json", ".mocharc.yaml", ".mocharc.yml",
	} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return Report{HasFramework: true, Framework: guessFrameworkFromConfig(name), Reason: "found " + name}, nil
		}
	}
	if depFramework := jsDepFramework(raw); depFramework != "" {
		return Report{HasFramework: true, Framework: depFramework, Reason: "devDependency " + depFramework}, nil
	}
	if scriptFW := jsTestScriptFramework(raw); scriptFW != "" {
		return Report{HasFramework: true, Framework: scriptFW, Reason: "scripts.test references " + scriptFW}, nil
	}
	return Report{HasFramework: false, Reason: "no test runner deps, scripts, or config found"}, nil
}

func guessFrameworkFromConfig(name string) string {
	switch {
	case strings.HasPrefix(name, "jest."):
		return "jest"
	case strings.HasPrefix(name, "vitest."):
		return "vitest"
	case strings.HasPrefix(name, "playwright."):
		return "playwright"
	case strings.HasPrefix(name, "cypress."):
		return "cypress"
	case strings.HasPrefix(name, "karma."):
		return "karma"
	case name == "jasmine.json":
		return "jasmine"
	case strings.HasPrefix(name, ".mocharc"):
		return "mocha"
	default:
		return "unknown"
	}
}

func jsDepFramework(packageJSON string) string {
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

func jsTestScriptFramework(packageJSON string) string {
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
	subs := []struct {
		sub, fw string
	}{
		{"jest", "jest"},
		{"vitest", "vitest"},
		{"mocha", "mocha"},
		{"jasmine", "jasmine"},
		{"ava", "ava"},
		{"playwright", "playwright"},
		{"cypress", "cypress"},
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

func detectJava(dir string) (Report, error) {
	jbf, err := primaryJavaBuildFile(dir)
	if err != nil {
		return Report{}, err
	}
	if jbf.Abs == "" {
		return Report{HasFramework: true, Reason: "no pom.xml or build.gradle under repo; skip java bootstrap"}, nil
	}
	switch jbf.Kind {
	case javaBuildMaven:
		b, err := os.ReadFile(jbf.Abs)
		if err != nil {
			return Report{}, err
		}
		s := strings.ToLower(string(b))
		if strings.Contains(s, "junit") || strings.Contains(s, "surefire") || strings.Contains(s, "failsafe") ||
			strings.Contains(s, "testng") {
			return Report{HasFramework: true, Framework: "junit", Reason: "pom.xml contains junit/surefire/failsafe/testng"}, nil
		}
		return Report{HasFramework: false, Reason: "pom.xml without obvious test plugins/deps"}, nil
	case javaBuildGradleGroovy, javaBuildGradleKotlin:
		name := filepath.Base(jbf.Abs)
		b, err := os.ReadFile(jbf.Abs)
		if err != nil {
			return Report{}, err
		}
		s := strings.ToLower(string(b))
		if strings.Contains(s, "junit") || strings.Contains(s, "testimplementation") ||
			strings.Contains(s, "testcompileonly") || strings.Contains(s, "testruntimeonly") {
			return Report{HasFramework: true, Framework: "junit", Reason: name + " mentions junit or test* dependencies"}, nil
		}
		return Report{HasFramework: false, Reason: name + " without obvious junit test deps"}, nil
	default:
		return Report{HasFramework: true, Reason: "no Java build file; skip java bootstrap"}, nil
	}
}

func detectCSharp(dir string) (Report, error) {
	csproj, err := primaryCsprojAbs(dir)
	if err != nil {
		return Report{}, err
	}
	if csproj == "" {
		return Report{HasFramework: true, Reason: "no SDK-style .csproj under repo; skip csharp bootstrap"}, nil
	}
	b, err := os.ReadFile(csproj)
	if err != nil {
		return Report{}, err
	}
	base := filepath.Base(csproj)
	if csprojHasDotNetTestFrameworkContent(string(b)) {
		return Report{HasFramework: true, Framework: "dotnet-test", Reason: base + " contains test SDK/framework"}, nil
	}
	return Report{HasFramework: false, Reason: base + " without xunit/nunit/mstest/test.sdk"}, nil
}
