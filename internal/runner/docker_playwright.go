package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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

// playwrightVersionRE accepts the plain x.y.z the Playwright image tags are built from.
var playwrightVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// PlaywrightDockerImageForVersion returns the official Playwright image whose bundled browsers
// match @playwright/test at `version`.
func PlaywrightDockerImageForVersion(version string) string {
	return "mcr.microsoft.com/playwright:v" + strings.TrimSpace(version) + "-jammy"
}

// InstalledPlaywrightTestVersion reads the version of @playwright/test a package directory actually
// resolved: node_modules/@playwright/test/package.json first (what the run will execute), then the
// package-lock.json entry (v2/v3 "packages", then v1 "dependencies"). "" when neither says.
//
// The browsers Playwright looks for are keyed by the installed npm version, and the official image
// ships exactly one set. In the asqs-core run of 2026-09-03 the bootstrap wrote `^1.49.1`, npm
// resolved a release wanting chromium_headless_shell-1234, and the image pinned for 1.49.1 could
// not have supplied it even once the image was actually used. Reading the resolved version and
// tagging the image from it removes the drift at its source.
func InstalledPlaywrightTestVersion(pkgDir string) string {
	dir := strings.TrimSpace(pkgDir)
	if dir == "" {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(dir, "node_modules", "@playwright", "test", "package.json")); err == nil {
		var pkg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &pkg) == nil && playwrightVersionRE.MatchString(strings.TrimSpace(pkg.Version)) {
			return strings.TrimSpace(pkg.Version)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package-lock.json")); err == nil {
		var lock struct {
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
			Dependencies map[string]struct {
				Version string `json:"version"`
			} `json:"dependencies"`
		}
		if json.Unmarshal(b, &lock) == nil {
			if e, ok := lock.Packages["node_modules/@playwright/test"]; ok && playwrightVersionRE.MatchString(strings.TrimSpace(e.Version)) {
				return strings.TrimSpace(e.Version)
			}
			if e, ok := lock.Dependencies["@playwright/test"]; ok && playwrightVersionRE.MatchString(strings.TrimSpace(e.Version)) {
				return strings.TrimSpace(e.Version)
			}
		}
	}
	return ""
}

// playwrightDockerImageRefFor is playwrightDockerImageRef for one package directory: an explicitly
// configured image still wins, otherwise the tag follows the @playwright/test version the directory
// resolved, and only a directory that says nothing falls back to the pinned default.
func (s *Sandbox) playwrightDockerImageRefFor(pkgDir string) string {
	if s != nil {
		if v := strings.TrimSpace(s.ImagePlaywright); v != "" {
			return v
		}
	}
	if v := InstalledPlaywrightTestVersion(pkgDir); v != "" {
		img := PlaywrightDockerImageForVersion(v)
		if img != DefaultPlaywrightDockerImage {
			fmt.Fprintf(os.Stderr, "[asqs-eval] E2E image follows the installed @playwright/test %s: %s (default would be %s)\n", v, img, DefaultPlaywrightDockerImage)
		}
		return img
	}
	return s.playwrightDockerImageRef()
}
