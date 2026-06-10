package runner

import "strings"

// DefaultPlaywrightDockerImage is the eval/E2E Docker image when runner.image_playwright is empty.
// Keep in sync with internal/testbootstrap DefaultPlaywrightDockerImage / @playwright/test pin.
const DefaultPlaywrightDockerImage = "mcr.microsoft.com/playwright:v1.49.1-jammy"

// DefaultPlaywrightJavaDockerImage is the eval Java E2E image when runner.image_playwright_java is empty.
// Keep in sync with internal/testbootstrap DefaultPlaywrightJavaDockerImage / Playwright Java pin.
const DefaultPlaywrightJavaDockerImage = "mcr.microsoft.com/playwright/java:v1.49.0-jammy"

// DefaultPlaywrightDotnetDockerImage is the C# E2E bootstrap image when runner.image_playwright_dotnet is empty.
// Keep tag aligned with internal/testbootstrap VersionMicrosoftPlaywrightNuGet.
const DefaultPlaywrightDotnetDockerImage = "mcr.microsoft.com/playwright/dotnet:v1.49.0-jammy"

func (s *Sandbox) playwrightDockerImageRef() string {
	if s == nil {
		return DefaultPlaywrightDockerImage
	}
	if v := strings.TrimSpace(s.ImagePlaywright); v != "" {
		return v
	}
	return DefaultPlaywrightDockerImage
}

// usePlaywrightDockerForJSE2E is true when the E2E pass should use the Playwright OCI image (browsers + Node).
// For lang csharp + e2eFramework playwright, the repo uses @playwright/test at the root (polyglot); Cypress is not
// implied for C# in this path.
func usePlaywrightDockerForJSE2E(lang, e2eFramework string) bool {
	fw := strings.ToLower(strings.TrimSpace(e2eFramework))
	switch fw {
	case "playwright", "cypress":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	case "csharp", "cs":
		return fw == "playwright"
	default:
		return false
	}
}

func (s *Sandbox) playwrightJavaDockerImageRef() string {
	if s == nil {
		return DefaultPlaywrightJavaDockerImage
	}
	if v := strings.TrimSpace(s.ImagePlaywrightJava); v != "" {
		return v
	}
	return DefaultPlaywrightJavaDockerImage
}

// usePlaywrightDockerForJavaE2E is true when the E2E pass should use the Playwright/Java OCI image (Chromium + system libs), not maven:*/gradle:* JDK-only images.
func usePlaywrightDockerForJavaE2E(lang, e2eFramework string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java":
	default:
		return false
	}
	return strings.ToLower(strings.TrimSpace(e2eFramework)) == "playwright-java"
}

func (s *Sandbox) playwrightDotnetDockerImageRef() string {
	if s == nil {
		return DefaultPlaywrightDotnetDockerImage
	}
	if v := strings.TrimSpace(s.ImagePlaywrightDotnet); v != "" {
		return v
	}
	return DefaultPlaywrightDotnetDockerImage
}

// usePlaywrightDockerForCSharpE2E is true when the E2E pass should use the Playwright/.NET OCI image (browsers + SDK), not plain mcr.microsoft.com/dotnet/sdk images.
func usePlaywrightDockerForCSharpE2E(lang, e2eFramework string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
	default:
		return false
	}
	return strings.ToLower(strings.TrimSpace(e2eFramework)) == "playwright-dotnet"
}

// dockerImageNeedsPlaywrightIPC returns true for official Playwright OCI images (Chromium stability).
func dockerImageNeedsPlaywrightIPC(image string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(image)), "playwright")
}

// dockerImageIsPlaywrightDotnet is true for mcr.microsoft.com/playwright/dotnet images (side-by-side .NET runtimes).
func dockerImageIsPlaywrightDotnet(image string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(image)), "playwright/dotnet")
}
