// Package testbootstrap detects missing test frameworks and can apply minimal Jest setup for JS/TS.
// Detection rules align with tools/js-ts-indexer/src/discovery.ts where applicable.
package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/layout"
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

// detectJava reports whether the module already carries the COMPLETE test stack its framework needs.
//
// The previous rule was a substring search: any pom mentioning "junit" or "surefire" counted as
// equipped. That is how a Spring Boot module with bare junit-jupiter passed detection, skipped
// bootstrap, and then produced twelve generated artifacts that could not compile because Mockito,
// AssertJ and org.springframework.boot.test were all absent. The question asked here is instead
// "which of the coordinates THIS framework requires are missing", so a partially equipped module is
// a bootstrap trigger rather than a skip.
func detectJava(dir string) (Report, error) {
	prof, jbf, err := resolveJavaTestProfile(dir)
	if err != nil {
		return Report{}, err
	}
	if jbf.Abs == "" {
		return Report{HasFramework: true, Reason: "no pom.xml or build.gradle under repo; skip java bootstrap"}, nil
	}
	if prof.Declined {
		return Report{HasFramework: true, Framework: string(prof.Framework), Reason: prof.DeclinedReason}, nil
	}

	b, err := os.ReadFile(jbf.Abs)
	if err != nil {
		return Report{}, err
	}
	src := string(b)
	name := filepath.Base(jbf.Abs)
	isMaven := jbf.Kind == javaBuildMaven

	missing := prof.missingDeps(src, isMaven)
	// Gradle defaults to the JUnit 4 runner: without useJUnitPlatform() a JUnit 5 suite compiles and
	// then executes zero tests, which every downstream step reads as "the tests passed".
	needsPlatformWiring := !isMaven && !strings.Contains(src, "useJUnitPlatform")

	if len(missing) == 0 && !needsPlatformWiring {
		return Report{
			HasFramework: true,
			Framework:    prof.Stack,
			Reason: fmt.Sprintf("%s already carries the full %s (%s) test stack: %s",
				name, prof.Framework, prof.Stack, strings.Join(describeJavaDeps(prof.Deps), ", ")),
		}, nil
	}

	var reason strings.Builder
	fmt.Fprintf(&reason, "%s is a %s module", name, prof.Framework)
	if len(missing) > 0 {
		fmt.Fprintf(&reason, " missing %s", strings.Join(describeJavaDeps(missing), ", "))
	}
	if needsPlatformWiring {
		if len(missing) > 0 {
			reason.WriteString(" and")
		}
		reason.WriteString(" missing useJUnitPlatform() (Gradle would run zero JUnit 5 tests)")
	}
	return Report{HasFramework: false, Framework: prof.Stack, Reason: reason.String()}, nil
}

// detectCSharp reports whether the solution already carries the COMPLETE test stack its framework
// needs.
//
// The previous rule was "does the primary .csproj reference Microsoft.NET.Test.Sdk / xunit / nunit /
// mstest". A test project with a bare xUnit reference satisfied it, so bootstrap skipped — and every
// generated test using Moq, FluentAssertions or WebApplicationFactory then failed to compile against
// a .csproj the fix loop is not allowed to write. This asks which of the coordinates the detected
// framework requires are actually missing.
func detectCSharp(dir string) (Report, error) {
	csproj, err := primaryCsprojAbs(dir)
	if err != nil {
		return Report{}, err
	}
	if csproj == "" {
		return Report{HasFramework: true, Reason: "no SDK-style .csproj under repo; skip csharp bootstrap"}, nil
	}

	prof, err := resolveCSharpTestProfile(dir, "")
	if err != nil {
		return Report{}, err
	}
	if prof.Declined {
		return Report{HasFramework: true, Framework: string(prof.Framework), Reason: prof.DeclinedReason}, nil
	}

	// No dedicated unit test project yet: bootstrap creates one with the full stack.
	testDirRel := layout.DetectCSharpUnitTestProjectDir(dir)
	if testDirRel == "" {
		if prod, _, derr := splitCSharpProdAndTestCsprojs(dir); derr == nil && len(prod) > 0 {
			return Report{
				HasFramework: false,
				Framework:    prof.Stack,
				Reason: fmt.Sprintf("%s solution with no unit test project; one will be created with %s",
					prof.Framework, strings.Join(describeCSharpPackages(prof.Packages), ", ")),
			}, nil
		}
	}

	target := csproj
	if testDirRel != "" {
		if tp := firstCsprojInDir(filepath.Join(dir, filepath.FromSlash(testDirRel))); tp != "" {
			target = tp
		}
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return Report{}, err
	}
	base := filepath.Base(target)
	missing := prof.missingPackages(string(b))
	if len(missing) == 0 {
		return Report{
			HasFramework: true,
			Framework:    prof.Stack,
			Reason: fmt.Sprintf("%s already carries the full %s (%s) test stack: %s",
				base, prof.Framework, prof.Stack, strings.Join(describeCSharpPackages(prof.Packages), ", ")),
		}, nil
	}
	return Report{
		HasFramework: false,
		Framework:    prof.Stack,
		Reason: fmt.Sprintf("%s is a %s solution missing %s",
			base, prof.Framework, strings.Join(describeCSharpPackages(missing), ", ")),
	}, nil
}
