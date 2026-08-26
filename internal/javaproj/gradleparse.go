package javaproj

import (
	"regexp"
	"strings"
)

// Gradle parsing is regex-only and covers both the Groovy and Kotlin DSLs. Version catalogs
// (libs.versions.toml) are deliberately out of scope: resolving `libs.junit.jupiter` means reading
// and interpreting a second file plus BOM-managed versions, which is build-system territory. Such
// coordinates are returned verbatim and labelled unresolved so the prompt never states a version
// that was guessed.
var (
	gradleLineCommentRE  = regexp.MustCompile(`(?m)//.*$`)
	gradleBlockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// id 'org.springframework.boot' version '3.2.5'   /   id("org.springframework.boot") version "3.2.5"
	gradleBootPluginRE = regexp.MustCompile(`id\s*\(?\s*['"]org\.springframework\.boot['"]\s*\)?\s*(?:version\s*)?\(?\s*['"]([^'"]+)['"]`)
	// sourceCompatibility = '17'  /  sourceCompatibility = JavaVersion.VERSION_17
	gradleSourceCompatRE = regexp.MustCompile(`(?i)(?:source|target)Compatibility\s*=?\s*(?:JavaVersion\.VERSION_)?['"]?([\d.]+)`)
	// languageVersion = JavaLanguageVersion.of(21)
	gradleToolchainRE = regexp.MustCompile(`JavaLanguageVersion\.of\(\s*(\d+)\s*\)`)
	// testImplementation 'org.mockito:mockito-core:5.0.0'  /  testImplementation(libs.junit.jupiter)
	gradleTestDepQuotedRE  = regexp.MustCompile(`test(?:Implementation|RuntimeOnly|CompileOnly|Api)\s*\(?\s*['"]([^'"]+)['"]`)
	gradleTestDepCatalogRE = regexp.MustCompile(`test(?:Implementation|RuntimeOnly|CompileOnly|Api)\s*\(\s*(libs\.[\w.]+)\s*\)`)
)

func stripGradleComments(src string) string {
	return gradleLineCommentRE.ReplaceAllString(gradleBlockCommentRE.ReplaceAllString(src, ""), "")
}

// GradleSpringBootVersion returns the Spring Boot plugin version, or "".
func GradleSpringBootVersion(src string) string {
	m := gradleBootPluginRE.FindStringSubmatch(stripGradleComments(src))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// GradleJavaVersion returns the configured Java language level, preferring an explicit toolchain.
func GradleJavaVersion(src string) string {
	clean := stripGradleComments(src)
	if m := gradleToolchainRE.FindStringSubmatch(clean); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := gradleSourceCompatRE.FindStringSubmatch(clean); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// GradleTestDependencies returns test-configuration coordinates. Version-catalog aliases are
// returned as-is (e.g. "libs.junit.jupiter") — the caller must label them unresolved.
func GradleTestDependencies(src string) []Dep {
	clean := stripGradleComments(src)
	var out []Dep
	seen := map[string]bool{}
	add := func(d Dep) {
		if d.ArtifactID == "" || seen[d.String()] {
			return
		}
		seen[d.String()] = true
		out = append(out, d)
	}
	for _, m := range gradleTestDepQuotedRE.FindAllStringSubmatch(clean, -1) {
		parts := strings.Split(strings.TrimSpace(m[1]), ":")
		switch len(parts) {
		case 1:
			add(Dep{ArtifactID: parts[0]})
		case 2:
			add(Dep{GroupID: parts[0], ArtifactID: parts[1]})
		default:
			add(Dep{GroupID: parts[0], ArtifactID: parts[1], Version: parts[2]})
		}
	}
	for _, m := range gradleTestDepCatalogRE.FindAllStringSubmatch(clean, -1) {
		add(Dep{ArtifactID: strings.TrimSpace(m[1])})
	}
	return out
}

// IsUnresolvedCatalogAlias reports whether a coordinate is a version-catalog reference rather than
// a real group:artifact.
func IsUnresolvedCatalogAlias(d Dep) bool {
	return d.GroupID == "" && strings.HasPrefix(d.ArtifactID, "libs.")
}
