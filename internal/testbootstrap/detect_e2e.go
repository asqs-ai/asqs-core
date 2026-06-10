package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// E2EReport is the outcome of DetectE2E.
type E2EReport struct {
	HasE2E    bool
	Framework string // JS/TS: playwright, cypress; Java: playwright-java, selenium, selenide; C#: playwright-dotnet, selenium
	Reason    string
}

// DetectE2E detects browser/E2E test stacks: JS/TS (Playwright/Cypress), Java (Playwright Java, Selenium, Selenide), C# (Microsoft.Playwright, Selenium).
func DetectE2E(repoPath, lang string) (E2EReport, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	dir := filepath.Clean(repoPath)
	switch lang {
	case "javascript", "typescript", "js", "ts":
		return detectE2EJS(dir)
	case "java":
		return detectE2EJava(dir)
	case "csharp", "cs":
		return detectE2ECSharp(dir)
	default:
		return E2EReport{HasE2E: false, Reason: "E2E detection not implemented for " + lang}, nil
	}
}

var (
	playwrightE2EConfigFiles = []string{"playwright.config.js", "playwright.config.ts", "playwright.config.mjs"}
	cypressE2EConfigFiles    = []string{"cypress.config.js", "cypress.config.ts", "cypress.config.mjs"}
)

func e2eConfigFilePresent(dir string, names []string) (bool, string) {
	for _, name := range names {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true, name
		}
	}
	return false, ""
}

func detectE2EJS(dir string) (E2EReport, error) {
	roots, err := jsPackageRootsForDetection(dir)
	if err != nil {
		return E2EReport{}, err
	}
	if len(roots) == 0 {
		return E2EReport{}, fmt.Errorf("no package.json under repo")
	}
	var last E2EReport
	for _, root := range roots {
		rep, err := detectE2EJSInPackageRoot(root)
		if err != nil {
			return E2EReport{}, err
		}
		if rep.HasE2E {
			return rep, nil
		}
		last = rep
	}
	return last, nil
}

func detectE2EJSInPackageRoot(dir string) (E2EReport, error) {
	pkgPath := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return E2EReport{}, err
	}
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return E2EReport{}, err
	}
	raw := string(data)

	hasPW, pwName := e2eConfigFilePresent(dir, playwrightE2EConfigFiles)
	hasCy, cyName := e2eConfigFilePresent(dir, cypressE2EConfigFiles)

	switch {
	case hasCy && !hasPW:
		return E2EReport{HasE2E: true, Framework: "cypress", Reason: "found " + cyName}, nil
	case hasPW && !hasCy:
		return E2EReport{HasE2E: true, Framework: "playwright", Reason: "found " + pwName}, nil
	}

	if fw := jsE2EScriptFramework(raw); fw != "" {
		return E2EReport{HasE2E: true, Framework: fw, Reason: "script references " + fw}, nil
	}
	if dep := jsE2EDepFramework(raw); dep != "" {
		return E2EReport{HasE2E: true, Framework: dep, Reason: "dependency " + dep}, nil
	}

	if hasCy && hasPW {
		return E2EReport{
			HasE2E: true, Framework: "cypress",
			Reason: "found both " + cyName + " and " + pwName + " without script/deps tie-break; preferring cypress (set runner.e2e_test_command to override)",
		}, nil
	}

	return E2EReport{HasE2E: false, Reason: "no E2E runner config, deps, or scripts detected"}, nil
}

func jsE2EDepFramework(packageJSON string) string {
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
	hasPW := has("@playwright/test") || has("playwright")
	hasCy := has("cypress")
	switch {
	case hasCy && !hasPW:
		return "cypress"
	case hasPW && !hasCy:
		return "playwright"
	case hasCy && hasPW:
		// Both listed (e.g. migration monorepo): prefer Cypress for the E2E pass unless scripts already chose; see detectE2EJS order.
		return "cypress"
	default:
		return ""
	}
}

func jsE2EScriptFramework(packageJSON string) string {
	var root struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(packageJSON), &root); err != nil {
		return ""
	}
	check := func(s string) string {
		low := strings.ToLower(strings.TrimSpace(s))
		if low == "" {
			return ""
		}
		if strings.Contains(low, "playwright") {
			return "playwright"
		}
		if strings.Contains(low, "cypress") {
			return "cypress"
		}
		return ""
	}
	if fw := check(root.Scripts["test"]); fw != "" {
		return fw
	}
	for _, name := range []string{"test:e2e", "e2e", "e2e:test", "pw:test"} {
		if fw := check(root.Scripts[name]); fw != "" {
			return fw
		}
	}
	// Named scripts missed: scan all values (e.g. "cy:run": "cypress run", "pw:e2e": "playwright test").
	return jsE2EScriptFrameworkScanAll(root.Scripts)
}

// jsE2EScriptFrameworkScanAll infers E2E stack from any package.json script body.
func jsE2EScriptFrameworkScanAll(scripts map[string]string) string {
	var sawCypress, sawPlaywright bool
	for _, s := range scripts {
		low := strings.ToLower(strings.TrimSpace(s))
		if low == "" {
			continue
		}
		if strings.Contains(low, "cypress") {
			sawCypress = true
		}
		if strings.Contains(low, "playwright") {
			sawPlaywright = true
		}
	}
	if sawCypress && !sawPlaywright {
		return "cypress"
	}
	if sawPlaywright && !sawCypress {
		return "playwright"
	}
	return ""
}
