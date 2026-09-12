package retrieval

import (
	"strings"
	"testing"
)

// The hints told every C# repo to write a browser test with Microsoft.Playwright. For a Web API
// with no pages that is a test that cannot be written: there is nothing to navigate to, and the
// model's only options are to invent a UI or produce a browser test that opens about:blank.
// WebApplicationFactory<Program> + HttpClient is the shape that actually tests such an application.
func TestE2EPromptCanonicalHints_csharpAPISurface(t *testing.T) {
	got := E2EPromptCanonicalHintsForSurface("csharp", "", "api")
	for _, want := range []string{"WebApplicationFactory", "HttpClient", "dotnet test"} {
		if !strings.Contains(got, want) {
			t.Errorf("api-surface hints missing %q:\n%s", want, got)
		}
	}
	// Naming the browser to rule it out is the point; describing how to drive one is not.
	for _, unwanted := range []string{"IPage", "Microsoft.Playwright", "IBrowser", "GotoAsync"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("api-surface hints describe a browser test (%q):\n%s", unwanted, got)
		}
	}
}

// A UI surface keeps the browser guidance and states the base-URL convention a generated test needs
// in order to reach a running application.
func TestE2EPromptCanonicalHints_csharpUISurface(t *testing.T) {
	for _, surface := range []string{"ui", "mixed"} {
		got := E2EPromptCanonicalHintsForSurface("csharp", "", surface)
		for _, want := range []string{"Microsoft.Playwright", "IPage", "ASQS_BASE_URL"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s-surface hints missing %q:\n%s", surface, want, got)
			}
		}
	}
}

// A detected framework wins over the surface on every surface but `api`, where the application has
// no pages for a browser stack to drive — see
// TestE2EPromptCanonicalHints_apiSurfaceOutranksADetectedBrowserFramework.
func TestE2EPromptCanonicalHints_detectedFrameworkWinsOverSurface(t *testing.T) {
	for _, surface := range []string{"ui", "mixed", "none", ""} {
		got := E2EPromptCanonicalHintsForSurface("csharp", "selenium", surface)
		if !strings.Contains(got, "OpenQA.Selenium") {
			t.Errorf("surface %q: detected selenium lost to the surface:\n%s", surface, got)
		}
	}
}

// An unknown surface keeps the previous behaviour rather than guessing.
func TestE2EPromptCanonicalHints_unknownSurfaceUnchanged(t *testing.T) {
	got := E2EPromptCanonicalHintsForSurface("csharp", "", "")
	old := E2EPromptCanonicalHints("csharp", "")
	if got != old {
		t.Errorf("an undetected surface changed the hints:\ngot:  %s\nwant: %s", got, old)
	}
}

// Other languages are untouched by the surface parameter.
func TestE2EPromptCanonicalHints_otherLanguagesIgnoreSurface(t *testing.T) {
	for _, lang := range []string{"java", "typescript"} {
		if got, want := E2EPromptCanonicalHintsForSurface(lang, "", "ui"), E2EPromptCanonicalHints(lang, ""); got != want {
			t.Errorf("%s hints changed with a surface:\n%s", lang, got)
		}
	}
}

// An `api` surface means the application has no pages. That fact outranks a detected browser
// framework, because ASQS's own E2E bootstrap installs Microsoft.Playwright for every C# repo today
// (CS21 is not done): on the second run of any bootstrapped Web API the framework is "explicitly"
// playwright-dotnet, and the API hints this exists to deliver were suppressed again — telling the
// model to drive pages that do not exist.
func TestE2EPromptCanonicalHints_apiSurfaceOutranksADetectedBrowserFramework(t *testing.T) {
	for _, fw := range []string{"playwright-dotnet", "playwright"} {
		got := E2EPromptCanonicalHintsForSurface("csharp", fw, "api")
		if !strings.Contains(got, "WebApplicationFactory") {
			t.Errorf("framework %q suppressed the api-surface guidance:\n%s", fw, got)
		}
		if strings.Contains(got, "IPage") {
			t.Errorf("framework %q reinstated browser guidance on a pageless application:\n%s", fw, got)
		}
	}
}

// A non-browser framework the repo already uses is still described on its own terms.
func TestE2EPromptCanonicalHints_apiSurfaceKeepsANonBrowserFramework(t *testing.T) {
	got := E2EPromptCanonicalHintsForSurface("csharp", "selenium", "ui")
	if !strings.Contains(got, "OpenQA.Selenium") {
		t.Errorf("a detected selenium stack lost to the ui surface:\n%s", got)
	}
}

// The ui hints name ASQS_BASE_URL, which nothing sets until CS24 lands. Until then a generated
// browser test must fail with that fact rather than navigate to an empty URL and time out — a
// timeout reads as a broken test, and the fixer spends rounds rewriting a test whose only problem
// is a missing precondition.
func TestE2EPromptCanonicalHints_uiSurfaceStatesTheBaseURLPrecondition(t *testing.T) {
	got := E2EPromptCanonicalHintsForSurface("csharp", "", "ui")
	if !strings.Contains(got, "ASQS_BASE_URL") {
		t.Fatalf("ui hints no longer name the base URL variable:\n%s", got)
	}
	for _, want := range []string{"unset", "fail"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("ui hints do not tell the test what to do when the variable is unset (%q):\n%s", want, got)
		}
	}
}
