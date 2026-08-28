// Package buildtool resolves which Java build tool a repository uses, for every part of the
// pipeline that needs to run one.
//
// It exists because four near-identical copies of this logic had drifted apart —
// runner.localBuildCommand, runner.javaBuildPrefix, evaluator.defaultJavaE2EShellCommand and
// postgenerate/staticcheck.javaStaticCheckCommand — with a fifth variant in
// evaluator/apisurface. Each re-derived Maven-vs-Gradle from the same files, and each disagreed
// with the others about wrappers and about Windows.
//
// # D3: never a repo wrapper
//
// Resolve returns "mvn" or "gradle", never "./mvnw", "./gradlew", "mvnw.cmd" or "gradlew.bat".
// The decision (D3 of the runner unification) is that both sandbox targets
// use the PATH/image binary, and U3b extended it to the rest of the pipeline so that a single run
// cannot bootstrap with the repository's pinned Maven and then evaluate with a different one.
//
// Two consequences worth stating plainly, because they were bugs before:
//
//   - **No runtime.GOOS branch.** Several of the old copies returned "mvnw.cmd" on a Windows host.
//     Two of them fed commands that run INSIDE A LINUX CONTAINER (the E2E pass and the format
//     step), so a Windows operator produced a container command naming a Windows batch file.
//     Host-shaped argv is an executor concern; it has no place in tool selection.
//   - **No exec.LookPath probe.** Whether the host has `mvn` says nothing about whether the
//     toolchain image does. Callers that run on the host should check availability themselves,
//     after resolution, and only when they are the ones executing.
package buildtool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the build system a repository uses.
type Kind string

const (
	None   Kind = ""
	Maven  Kind = "maven"
	Gradle Kind = "gradle"
)

// Tool is a resolved build tool.
type Tool struct {
	Kind Kind
	// Binary is the executable name, always resolved from PATH (or supplied by a container image).
	Binary string
	// BuildFile is the repo-relative file that selected this tool, for diagnostics.
	BuildFile string
}

// ErrNoBuildFile means the directory holds neither a Maven nor a Gradle build file.
var ErrNoBuildFile = errors.New("no pom.xml or build.gradle in the repository")

// Canonicalize maps a configured general.build.build_tool value onto {auto, mvn, gradle}, reporting
// whether it was one of the deprecated wrapper aliases so the caller can warn once at startup.
// An unrecognised value is returned unchanged with ok=false so the caller can reject it.
func Canonicalize(configured string) (canonical string, wasWrapperAlias, ok bool) {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "", "auto":
		return "auto", false, true
	case "mvn", "maven":
		return "mvn", false, true
	case "gradle":
		return "gradle", false, true
	case "mvnw":
		return "mvn", true, true
	case "gradlew":
		return "gradle", true, true
	default:
		return strings.TrimSpace(configured), false, false
	}
}

// Resolve picks the build tool for dir. configured is general.build.build_tool ("auto" when empty).
//
// With "auto", a repository holding both a pom.xml and a Gradle build file resolves to Maven,
// matching the precedence every previous copy used.
func Resolve(dir, configured string) (Tool, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	canonical, _, ok := Canonicalize(configured)
	if !ok {
		return Tool{}, fmt.Errorf("build_tool must be auto, mvn or gradle (mvnw and gradlew are deprecated aliases); got %q", configured)
	}

	pom := fileExists(filepath.Join(dir, "pom.xml"))
	gradleFile, hasGradle := gradleBuildFile(dir)

	switch canonical {
	case "mvn":
		if !pom {
			return Tool{}, fmt.Errorf("build_tool is mvn but no pom.xml in %s", dir)
		}
		return Tool{Kind: Maven, Binary: "mvn", BuildFile: "pom.xml"}, nil
	case "gradle":
		if !hasGradle {
			return Tool{}, fmt.Errorf("build_tool is gradle but no build.gradle in %s", dir)
		}
		return Tool{Kind: Gradle, Binary: "gradle", BuildFile: gradleFile}, nil
	}

	switch {
	case pom:
		return Tool{Kind: Maven, Binary: "mvn", BuildFile: "pom.xml"}, nil
	case hasGradle:
		return Tool{Kind: Gradle, Binary: "gradle", BuildFile: gradleFile}, nil
	default:
		return Tool{}, fmt.Errorf("%w: %s", ErrNoBuildFile, dir)
	}
}

// gradleBuildFile reports the Gradle build file present in dir, Groovy before Kotlin.
func gradleBuildFile(dir string) (string, bool) {
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if fileExists(filepath.Join(dir, name)) {
			return name, true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
