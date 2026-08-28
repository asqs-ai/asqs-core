package testbootstrap

import (
	"os"
	"strings"
)

// renderGradleDep emits one dependency line for the Groovy or Kotlin DSL. A dep with no Version is
// rendered without one so the framework's BOM (Spring's dependency-management plugin, the Quarkus or
// Micronaut platform) supplies it.
func renderGradleDep(d javaDep, kotlinDSL bool) string {
	cfg := "testImplementation"
	if d.RuntimeOnly {
		cfg = "testRuntimeOnly"
	}
	coord := d.coord()
	if d.Version != "" {
		coord += ":" + d.Version
	}
	if kotlinDSL {
		return "    " + cfg + `("` + coord + `")`
	}
	return "    " + cfg + " '" + coord + "'"
}

// renderGradleDependenciesBlock builds the appended block. Gradle merges multiple dependencies {}
// blocks, so appending is safe on a build file that already has one.
func renderGradleDependenciesBlock(deps []javaDep, kotlinDSL bool, stack string) string {
	var b strings.Builder
	b.WriteString("\n// ASQS test_framework_bootstrap: " + stack + "\n")
	b.WriteString("dependencies {\n")
	for _, d := range deps {
		b.WriteString(renderGradleDep(d, kotlinDSL) + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// gradleUseJUnitPlatformBlock is appended when the build file never calls useJUnitPlatform().
//
// This is the quietest failure in the Gradle path: without it Gradle drives tests with the JUnit 4
// runner, JUnit 5 classes match nothing, and the build reports success having executed zero tests.
// The bootstrap smoke test turns that into a visible failure, and this block prevents it.
func gradleUseJUnitPlatformBlock(kotlinDSL bool) string {
	if kotlinDSL {
		return "\n// ASQS test_framework_bootstrap: run JUnit 5 (Gradle defaults to the JUnit 4 runner)\ntasks.withType<Test>().configureEach {\n    useJUnitPlatform()\n}\n"
	}
	return "\n// ASQS test_framework_bootstrap: run JUnit 5 (Gradle defaults to the JUnit 4 runner)\ntasks.withType(Test).configureEach {\n    useJUnitPlatform()\n}\n"
}

// applyGradleTestDeps appends the profile's missing test dependencies and, when absent,
// useJUnitPlatform() wiring.
func applyGradleTestDeps(path string, kotlinDSL bool, prof javaTestProfile) (changed bool, added []javaDep, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, nil, err
	}
	s := string(b)
	orig := s

	missing := prof.missingDeps(s, false)
	needsPlatform := !strings.Contains(s, "useJUnitPlatform")
	if len(missing) == 0 && !needsPlatform {
		return false, nil, nil
	}

	if !strings.HasSuffix(strings.TrimSpace(s), "\n") {
		s += "\n"
	}
	if len(missing) > 0 {
		s += renderGradleDependenciesBlock(missing, kotlinDSL, prof.Stack)
		added = missing
	}
	if needsPlatform {
		s += gradleUseJUnitPlatformBlock(kotlinDSL)
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s == orig {
		return false, nil, nil
	}
	return true, added, atomicWrite(path, []byte(s))
}
