package javaproj

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// Multi-module repos: the module's own pom carries the versions that apply to its sources. The
// previous code only ever stat'd the repository root.
func TestNearestBuildFileRel_prefersModulePom(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", "<project/>")
	writeFile(t, root, "mod-a/pom.xml", "<project/>")

	rel, kind, ok := NearestBuildFileRel(root, "mod-a/src/main/java/p/Foo.java")
	if !ok || rel != "mod-a/pom.xml" || kind != BuildMaven {
		t.Fatalf("got (%q, %v, %v), want mod-a/pom.xml", rel, kind, ok)
	}
}

func TestNearestBuildFileRel_fallsBackToRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", "<project/>")

	rel, _, ok := NearestBuildFileRel(root, "src/main/java/p/Foo.java")
	if !ok || rel != "pom.xml" {
		t.Fatalf("got (%q, %v), want pom.xml", rel, ok)
	}
}

func TestNearestBuildFileRel_detectsKotlinDSL(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "build.gradle.kts", "plugins {}")

	rel, kind, ok := NearestBuildFileRel(root, "src/main/java/p/Foo.java")
	if !ok || rel != "build.gradle.kts" || kind != BuildGradleKotlin {
		t.Fatalf("got (%q, %v, %v)", rel, kind, ok)
	}
}

func TestNearestBuildFileRel_noneFound(t *testing.T) {
	if _, _, ok := NearestBuildFileRel(t.TempDir(), "src/Foo.java"); ok {
		t.Fatal("expected no build file")
	}
}

// A module pom that omits java.version inherits it from the parent, one hop up.
func TestResolve_inheritsFromAncestorPom(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", `<project><properties><java.version>17</java.version></properties></project>`)
	writeFile(t, root, "mod-a/pom.xml", `<project>
	  <parent><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-parent</artifactId><version>3.2.5</version></parent>
	</project>`)

	f := Resolve(root, "mod-a/src/main/java/p/Foo.java")
	if f.BuildFileRel != "mod-a/pom.xml" {
		t.Fatalf("BuildFileRel = %q", f.BuildFileRel)
	}
	if f.SpringBootVer != "3.2.5" {
		t.Errorf("SpringBootVer = %q, want 3.2.5", f.SpringBootVer)
	}
	if f.JavaVersion != "17" {
		t.Errorf("JavaVersion = %q, want 17 (inherited from the parent pom)", f.JavaVersion)
	}
	if f.AncestorPomRel != "pom.xml" {
		t.Errorf("AncestorPomRel = %q, want pom.xml", f.AncestorPomRel)
	}
}

func TestResolve_gradleModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "build.gradle", groovyGradle)

	f := Resolve(root, "src/main/java/p/Foo.java")
	if !f.Found() {
		t.Fatal("expected facts from a gradle build file")
	}
	if f.JavaVersion != "17" || f.SpringBootVer != "3.2.5" {
		t.Fatalf("java=%q boot=%q", f.JavaVersion, f.SpringBootVer)
	}
}

func TestFacts_NotFoundWhenNothingUseful(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pom.xml", `<project></project>`)
	if Resolve(root, "src/Foo.java").Found() {
		t.Fatal("an empty pom carries no facts worth putting in the prompt")
	}
}
