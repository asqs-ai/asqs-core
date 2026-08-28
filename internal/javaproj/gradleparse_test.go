package javaproj

import "testing"

const groovyGradle = `
plugins {
    id 'org.springframework.boot' version '3.2.5'
    id 'java'
}
java { sourceCompatibility = '17' }
dependencies {
    testImplementation 'org.springframework.boot:spring-boot-starter-test'
    testImplementation 'org.mockito:mockito-core:5.3.1'
    // testImplementation 'should:be-ignored:1.0'
}
`

const kotlinGradle = `
plugins {
    id("org.springframework.boot") version "3.3.0"
}
java { toolchain { languageVersion = JavaLanguageVersion.of(21) } }
dependencies {
    testImplementation(libs.junit.jupiter)
    testImplementation("org.assertj:assertj-core:3.25.1")
}
`

func TestGradleSpringBootVersion(t *testing.T) {
	if got := GradleSpringBootVersion(groovyGradle); got != "3.2.5" {
		t.Errorf("groovy: got %q, want 3.2.5", got)
	}
	if got := GradleSpringBootVersion(kotlinGradle); got != "3.3.0" {
		t.Errorf("kotlin: got %q, want 3.3.0", got)
	}
	if got := GradleSpringBootVersion("plugins { id 'java' }"); got != "" {
		t.Errorf("no boot plugin: got %q, want empty", got)
	}
}

func TestGradleJavaVersion(t *testing.T) {
	if got := GradleJavaVersion(groovyGradle); got != "17" {
		t.Errorf("sourceCompatibility: got %q, want 17", got)
	}
	if got := GradleJavaVersion(kotlinGradle); got != "21" {
		t.Errorf("toolchain: got %q, want 21", got)
	}
	if got := GradleJavaVersion("plugins { id 'java' }"); got != "" {
		t.Errorf("absent: got %q", got)
	}
}

func TestGradleTestDependencies(t *testing.T) {
	deps := GradleTestDependencies(groovyGradle)
	if len(deps) != 2 {
		t.Fatalf("groovy: got %d deps (commented line must be ignored): %v", len(deps), deps)
	}
	if deps[0].String() != "org.springframework.boot:spring-boot-starter-test" {
		t.Errorf("groovy dep[0] = %q", deps[0].String())
	}

	kdeps := GradleTestDependencies(kotlinGradle)
	if len(kdeps) != 2 {
		t.Fatalf("kotlin: got %d deps: %v", len(kdeps), kdeps)
	}
	var sawCatalog bool
	for _, d := range kdeps {
		if IsUnresolvedCatalogAlias(d) {
			sawCatalog = true
			if d.ArtifactID != "libs.junit.jupiter" {
				t.Errorf("catalog alias mangled: %q", d.ArtifactID)
			}
		}
	}
	if !sawCatalog {
		t.Error("version-catalog alias was dropped instead of being returned verbatim")
	}
}
