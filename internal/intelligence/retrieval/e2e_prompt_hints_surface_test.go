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

// An explicitly detected framework still wins over the surface: a repo that already uses Selenium
// gets Selenium guidance whatever its pages look like.
func TestE2EPromptCanonicalHints_detectedFrameworkWinsOverSurface(t *testing.T) {
	got := E2EPromptCanonicalHintsForSurface("csharp", "selenium", "api")
	if !strings.Contains(got, "OpenQA.Selenium") {
		t.Errorf("detected selenium lost to the surface:\n%s", got)
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
