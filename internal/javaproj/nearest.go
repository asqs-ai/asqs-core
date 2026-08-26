// Package javaproj extracts the Java/JVM build facts a test generator needs to emit
// version-correct code: the language level and the framework versions in effect for a given source
// file.
//
// It is deliberately NOT a build system. There is no POM inheritance resolution from disk or
// ~/.m2, no Gradle evaluation, no dependency graph. Regex plus one hop of property substitution is
// enough to answer the only question being asked — "which major versions is this repo on?" — and
// mirrors what internal/dotnetproj already does for .csproj.
//
// It exists because generated Java tests were importing Spring Boot 3 coordinates
// (org.springframework.boot.test.mock.mockito.MockBean, …autoconfigure.web.servlet.WebMvcTest)
// into a repo on a newer major version where those packages had moved, producing
// "package … does not exist" errors the fixer could not repair. C# already had this grounding via
// dotnetproj's TFM block; Java had nothing.
package javaproj

import (
	"os"
	"path/filepath"
	"strings"
)

// BuildKind identifies which build tool a discovered file belongs to.
type BuildKind string

const (
	BuildMaven        BuildKind = "maven"
	BuildGradleGroovy BuildKind = "gradle"
	BuildGradleKotlin BuildKind = "gradle-kts"
)

// buildFileNames is the search order at each directory level. Maven first: when a repo has both,
// pom.xml is the authoritative one for versions.
var buildFileNames = []struct {
	name string
	kind BuildKind
}{
	{"pom.xml", BuildMaven},
	{"build.gradle", BuildGradleGroovy},
	{"build.gradle.kts", BuildGradleKotlin},
}

// NearestBuildFileRel walks up from sourceFileRel's directory to repoRoot looking for a build file,
// falling back to the repository root. Returns a repo-relative slash path.
//
// Walking up matters for multi-module repos: the module's own pom.xml carries the versions that
// apply to its sources, and the previous code only ever stat'd the repository root.
func NearestBuildFileRel(repoRoot, sourceFileRel string) (string, BuildKind, bool) {
	repoRoot = filepath.Clean(repoRoot)
	rel := filepath.ToSlash(strings.TrimSpace(sourceFileRel))
	dir := ""
	if rel != "" {
		dir = filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
	}
	for {
		for _, bf := range buildFileNames {
			candidate := bf.name
			if dir != "" {
				candidate = dir + "/" + bf.name
			}
			if fileExists(filepath.Join(repoRoot, filepath.FromSlash(candidate))) {
				return candidate, bf.kind, true
			}
		}
		if dir == "" {
			return "", "", false
		}
		next := filepath.ToSlash(filepath.Dir(dir))
		if next == dir || next == "." || next == "/" {
			dir = ""
			continue
		}
		dir = next
	}
}

// AncestorPomRel returns the next pom.xml above the given one, or "" at the repository root.
// One hop only: it covers the standard parent/module Maven layout and stops, rather than becoming
// a POM resolver.
func AncestorPomRel(repoRoot, pomRel string) (string, bool) {
	dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(pomRel)))
	for dir != "." && dir != "" && dir != "/" {
		dir = filepath.ToSlash(filepath.Dir(dir))
		candidate := "pom.xml"
		if dir != "." && dir != "" && dir != "/" {
			candidate = dir + "/pom.xml"
		}
		if fileExists(filepath.Join(repoRoot, filepath.FromSlash(candidate))) {
			return candidate, true
		}
		if dir == "." || dir == "" || dir == "/" {
			break
		}
	}
	return "", false
}

func fileExists(abs string) bool {
	st, err := os.Stat(abs)
	return err == nil && !st.IsDir()
}

// ReadIfPresent returns a file's contents, or "" when it cannot be read.
func ReadIfPresent(repoRoot, rel string) string {
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(b)
}
