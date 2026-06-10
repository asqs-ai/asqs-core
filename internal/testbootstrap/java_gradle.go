package testbootstrap

import (
	"os"
	"strings"
)

const gradleGroovyDeps = `
// ASQS test_framework_bootstrap: JUnit 5
dependencies {
    testImplementation 'org.junit.jupiter:junit-jupiter:` + VersionJUnitJupiter + `'
    testRuntimeOnly 'org.junit.platform:junit-platform-launcher:` + VersionJUnitPlatform + `'
}
`

const gradleKotlinDeps = `
// ASQS test_framework_bootstrap: JUnit 5
dependencies {
    testImplementation("org.junit.jupiter:junit-jupiter:` + VersionJUnitJupiter + `")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher:` + VersionJUnitPlatform + `")
}
`

// applyGradleJUnit appends a dependencies block with JUnit 5 (Gradle merges multiple dependencies {}).
func applyGradleJUnit(path string, kotlinDSL bool) (changed bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, "junit-jupiter") {
		return false, nil
	}
	block := gradleGroovyDeps
	if kotlinDSL {
		block = gradleKotlinDeps
	}
	if !strings.HasSuffix(strings.TrimSpace(s), "\n") {
		s += "\n"
	}
	s += block
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return true, atomicWrite(path, []byte(s))
}
