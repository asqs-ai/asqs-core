package testbootstrap

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Framework IDs for non-JS E2E (stored in E2EReport.Framework; used by evaluator + LLM context).
const (
	E2EFrameworkPlaywrightJava   = "playwright-java"
	E2EFrameworkPlaywrightDotnet = "playwright-dotnet"
	E2EFrameworkSelenium         = "selenium"
	E2EFrameworkSelenide         = "selenide"
)

func detectE2EJava(dir string) (E2EReport, error) {
	dir = filepath.Clean(dir)
	poms, err := discoverPomXMLPaths(dir)
	if err != nil {
		return E2EReport{}, err
	}
	for _, pom := range poms {
		b, err := os.ReadFile(pom)
		if err != nil {
			return E2EReport{}, err
		}
		rel, _ := filepath.Rel(dir, pom)
		if rel == "" {
			rel = "pom.xml"
		}
		rep := e2eReportFromJavaPOM(string(b), filepath.ToSlash(rel))
		if rep.HasE2E {
			return rep, nil
		}
	}
	gradles, err := discoverGradlePaths(dir)
	if err != nil {
		return E2EReport{}, err
	}
	for _, g := range gradles {
		b, err := os.ReadFile(g)
		if err != nil {
			return E2EReport{}, err
		}
		rel, _ := filepath.Rel(dir, g)
		if rel == "" {
			rel = filepath.Base(g)
		}
		rep := e2eReportFromJavaGradle(string(b), filepath.ToSlash(rel))
		if rep.HasE2E {
			return rep, nil
		}
	}
	if len(poms) == 0 && len(gradles) == 0 {
		return E2EReport{HasE2E: false, Reason: "no pom.xml or Gradle file found under repo for Java E2E detection"}, nil
	}
	return E2EReport{HasE2E: false, Reason: "no Playwright/Selenium/Selenide in discovered Java build files"}, nil
}

func e2eReportFromJavaPOM(s, label string) E2EReport {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "com.microsoft.playwright") || strings.Contains(low, "microsoft.playwright"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkPlaywrightJava, Reason: label + " contains Playwright Java dependency"}
	case strings.Contains(low, "selenide"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenide, Reason: label + " contains Selenide"}
	case strings.Contains(low, "selenium-java") || strings.Contains(low, "org.seleniumhq.selenium"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenium, Reason: label + " contains Selenium"}
	case strings.Contains(low, "failsafe") && (strings.Contains(low, "playwright") || strings.Contains(low, "selenium") || strings.Contains(low, "webdriver")):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenium, Reason: label + " has failsafe with browser/E2E hints"}
	default:
		return E2EReport{HasE2E: false, Reason: label + " has no Playwright/Selenium/Selenide signals"}
	}
}

func e2eReportFromJavaGradle(s, label string) E2EReport {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "com.microsoft.playwright") || strings.Contains(low, "microsoft.playwright"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkPlaywrightJava, Reason: label + " contains Playwright Java"}
	case strings.Contains(low, "selenide"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenide, Reason: label + " contains Selenide"}
	case strings.Contains(low, "selenium"):
		return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenium, Reason: label + " contains Selenium"}
	}
	return E2EReport{HasE2E: false, Reason: label + " has no Playwright/Selenium/Selenide signals"}
}

// e2eNodePlaywrightAtRepoRoot is true when the repo uses Playwright via Node (@playwright/test) at the root or
// under e2e/, without requiring Microsoft.Playwright in a .csproj. Polyglot repos (e.g. github.com/dotnet/eShop)
// often keep Playwright in TypeScript while C# is the indexed language — we must not run C# NuGet Playwright bootstrap.
func e2eNodePlaywrightAtRepoRoot(dir string) (E2EReport, bool) {
	dir = filepath.Clean(dir)
	for _, sub := range []string{dir, filepath.Join(dir, "e2e")} {
		if ok, name := e2eConfigFilePresent(sub, playwrightE2EConfigFiles); ok {
			rel, _ := filepath.Rel(dir, filepath.Join(sub, name))
			if rel == "." || rel == "" {
				rel = name
			}
			return E2EReport{
				HasE2E:    true,
				Framework: "playwright",
				Reason:    filepath.ToSlash(rel) + " (Node Playwright; not Microsoft.Playwright/.NET)",
			}, true
		}
	}
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return E2EReport{}, false
	}
	raw := string(data)
	if dep := jsE2EDepFramework(raw); dep == "playwright" {
		return E2EReport{HasE2E: true, Framework: "playwright", Reason: "package.json depends on @playwright/test or playwright (Node)"}, true
	}
	if fw := jsE2EScriptFramework(raw); fw == "playwright" {
		return E2EReport{HasE2E: true, Framework: "playwright", Reason: "package.json scripts reference Playwright (Node)"}, true
	}
	// Root package-lock references @playwright/test (e.g. hoisted or lock-only signal).
	if b, err := os.ReadFile(filepath.Join(dir, "package-lock.json")); err == nil && lockMentionsPlaywrightTest(string(b)) {
		return E2EReport{HasE2E: true, Framework: "playwright", Reason: "package-lock.json references @playwright/test (Node)"}, true
	}
	return E2EReport{}, false
}

func lockMentionsPlaywrightTest(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, `"@playwright/test"`) || strings.Contains(low, `"node_modules/@playwright/test"`)
}

func detectE2ECSharp(dir string) (E2EReport, error) {
	dir = filepath.Clean(dir)
	if rep, ok := e2eNodePlaywrightAtRepoRoot(dir); ok {
		return rep, nil
	}
	paths, err := discoverCsprojPaths(dir)
	if err != nil {
		return E2EReport{}, err
	}
	if len(paths) == 0 {
		return E2EReport{HasE2E: false, Reason: "no .csproj found under repo for C# E2E detection"}, nil
	}
	sort.Strings(paths)
	for _, abs := range paths {
		b, err := os.ReadFile(abs)
		if err != nil {
			return E2EReport{}, err
		}
		rel, _ := filepath.Rel(dir, abs)
		if rel == "" {
			rel = filepath.Base(abs)
		}
		label := filepath.ToSlash(rel)
		low := strings.ToLower(string(b))
		switch {
		case strings.Contains(low, "microsoft.playwright"):
			return E2EReport{HasE2E: true, Framework: E2EFrameworkPlaywrightDotnet, Reason: label + " references Microsoft.Playwright"}, nil
		case strings.Contains(low, "selenium.webdriver"):
			return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenium, Reason: label + " references Selenium.WebDriver"}, nil
		case strings.Contains(low, "selenium"):
			return E2EReport{HasE2E: true, Framework: E2EFrameworkSelenium, Reason: label + " references Selenium"}, nil
		}
	}
	return E2EReport{HasE2E: false, Reason: "no Microsoft.Playwright or Selenium in discovered .csproj files"}, nil
}
