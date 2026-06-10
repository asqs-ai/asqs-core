// Package profile defines language-specific execution settings for sandbox jobs (images, caches, reports, heuristics).
package profile

import "strings"

// LanguageProfile configures how evaluation runs for one language in an isolated job.
type LanguageProfile struct {
	Lang string
	// DefaultImage when config does not override (runner.image_java, etc.).
	DefaultImage string
	// CacheMounts suggested named-volume targets (Source left empty = filled from config volume names).
	CacheContainerPaths []string // e.g. "/root/.m2", "/root/.npm"
	// ReportPaths are repo-relative globs or fixed paths where coverage reports often appear (for summaries / heuristics).
	ReportPaths []string
	// TestFrameworkHints keywords in package.json or pom for framework detection (documentation / future use).
	TestFrameworkHints []string
}

// Profiles by normalized language key.
var Profiles = map[string]LanguageProfile{
	"java": {
		Lang:                "java",
		DefaultImage:        "eclipse-temurin:21-jdk",
		CacheContainerPaths: []string{"/root/.m2"},
		ReportPaths:         []string{"target/site/jacoco/*/index.html", "build/reports/jacoco/test/html/index.html"},
		TestFrameworkHints:  []string{"junit", "testng", "surefire"},
	},
	"javascript": {
		Lang:                "javascript",
		DefaultImage:        "node:20-bookworm",
		CacheContainerPaths: []string{"/root/.npm", "/root/.cache/yarn", "/root/.local/share/pnpm/store"},
		ReportPaths:         []string{"coverage/lcov.info", "coverage/index.html"},
		TestFrameworkHints:  []string{"jest", "vitest", "mocha", "karma"},
	},
	"typescript": {
		Lang:                "typescript",
		DefaultImage:        "node:20-bookworm",
		CacheContainerPaths: []string{"/root/.npm", "/root/.cache/yarn", "/root/.local/share/pnpm/store"},
		ReportPaths:         []string{"coverage/lcov.info", "coverage/index.html"},
		TestFrameworkHints:  []string{"jest", "vitest", "mocha"},
	},
	"csharp": {
		Lang:                "csharp",
		DefaultImage:        "mcr.microsoft.com/dotnet/sdk:10.0",
		CacheContainerPaths: []string{"/root/.nuget/packages"},
		ReportPaths:         []string{"TestResults/*/coverage.cobertura.xml"},
		TestFrameworkHints:  []string{"xunit", "nunit", "mstest"},
	},
}

// ForLang returns the profile for lang, or a minimal default.
func ForLang(lang string) LanguageProfile {
	k := strings.ToLower(strings.TrimSpace(lang))
	if p, ok := Profiles[k]; ok {
		return p
	}
	return LanguageProfile{
		Lang:         k,
		DefaultImage: "eclipse-temurin:21-jdk",
	}
}

// ImageFor resolves the container image: config override per language, else profile default.
func ImageFor(lang, imageJava, imageDotNet, imageNode string) string {
	k := strings.ToLower(strings.TrimSpace(lang))
	switch k {
	case "java":
		if s := strings.TrimSpace(imageJava); s != "" {
			return s
		}
	case "csharp", "cs":
		if s := strings.TrimSpace(imageDotNet); s != "" {
			return s
		}
	case "javascript", "typescript", "js", "ts":
		if s := strings.TrimSpace(imageNode); s != "" {
			return s
		}
	}
	return ForLang(lang).DefaultImage
}
