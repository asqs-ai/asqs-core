package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJavaSource(t *testing.T, moduleRoot, relPkgDir, name, body string) {
	t.Helper()
	dir := filepath.Join(moduleRoot, "src", "main", "java", filepath.FromSlash(relPkgDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJavaApplicationBasePackage_findsSpringBootApplication(t *testing.T) {
	root := t.TempDir()
	writeJavaSource(t, root, "com/example/javatest", "JavaTestApplication.java",
		"package com.example.javatest;\n\n@SpringBootApplication\npublic class JavaTestApplication {}\n")
	writeJavaSource(t, root, "com/example/javatest/api", "OrderController.java",
		"package com.example.javatest.api;\n\npublic class OrderController {}\n")

	pkg, ok := javaApplicationBasePackage(root, "@SpringBootApplication")
	if !ok || pkg != "com.example.javatest" {
		t.Fatalf("pkg = %q ok = %v; want com.example.javatest", pkg, ok)
	}
}

func TestJavaApplicationBasePackage_prefersShallowestPackage(t *testing.T) {
	root := t.TempDir()
	writeJavaSource(t, root, "com/example/deep/nested", "NestedApp.java",
		"package com.example.deep.nested;\n@SpringBootApplication\npublic class NestedApp {}\n")
	writeJavaSource(t, root, "com/example", "RootApp.java",
		"package com.example;\n@SpringBootApplication\npublic class RootApp {}\n")

	pkg, ok := javaApplicationBasePackage(root, "@SpringBootApplication")
	if !ok || pkg != "com.example" {
		t.Fatalf("pkg = %q; want the shallowest package com.example (its scan covers the rest)", pkg)
	}
}

func TestJavaApplicationBasePackage_libraryModuleHasNone(t *testing.T) {
	root := t.TempDir()
	writeJavaSource(t, root, "com/example/lib", "Util.java", "package com.example.lib;\npublic class Util {}\n")
	if _, ok := javaApplicationBasePackage(root, "@SpringBootApplication"); ok {
		t.Fatal("a library module has no application class to boot")
	}
}

// TestWriteJavaFrameworkSmokeTest_springLandsInAppPackage guards the constraint that makes the
// Spring smoke meaningful: @SpringBootTest walks UP the package tree for @SpringBootConfiguration,
// so a smoke test parked in com.asqs.bootstrap fails no matter how correct the dependencies are.
func TestWriteJavaFrameworkSmokeTest_springLandsInAppPackage(t *testing.T) {
	root := t.TempDir()
	writeJavaSource(t, root, "com/example/javatest", "JavaTestApplication.java",
		"package com.example.javatest;\n@SpringBootApplication\npublic class JavaTestApplication {}\n")

	f, staged, err := writeJavaFrameworkSmokeTest(root, javaSmokeSpringBoot)
	if err != nil || !staged {
		t.Fatalf("staged = %v err = %v", staged, err)
	}
	wantPath := filepath.Join(root, "src", "test", "java", "com", "example", "javatest", "AsqsFrameworkSmokeTest.java")
	if f.Abs != wantPath {
		t.Fatalf("path = %s\nwant   %s", f.Abs, wantPath)
	}
	if f.FQCN != "com.example.javatest.AsqsFrameworkSmokeTest" {
		t.Errorf("FQCN = %s", f.FQCN)
	}
	b, err := os.ReadFile(f.Abs)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.HasPrefix(src, "package com.example.javatest;") {
		t.Errorf("package token not substituted:\n%s", src)
	}
	if strings.Contains(src, javaSmokePackageToken) {
		t.Error("template placeholder survived into the written file")
	}
}

func TestWriteJavaFrameworkSmokeTest_springSkippedWithoutApplicationClass(t *testing.T) {
	root := t.TempDir()
	writeJavaSource(t, root, "com/example/lib", "Util.java", "package com.example.lib;\npublic class Util {}\n")
	if _, staged, err := writeJavaFrameworkSmokeTest(root, javaSmokeSpringBoot); staged || err != nil {
		t.Fatalf("staged = %v err = %v; a library module must skip the framework smoke", staged, err)
	}
}

func TestWriteJavaUnitSmokeTest_writesOnceAndReportsFQCN(t *testing.T) {
	root := t.TempDir()
	f, err := writeJavaUnitSmokeTest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Wrote {
		t.Fatal("expected the smoke test to be written")
	}
	if f.FQCN != "com.asqs.bootstrap.AsqsBootstrapSmokeTest" {
		t.Fatalf("FQCN = %s", f.FQCN)
	}
	b, _ := os.ReadFile(f.Abs)
	src := string(b)
	for _, want := range []string{"org.junit.jupiter.api.Test", "org.mockito.Mockito.mock", "org.assertj.core.api.Assertions.assertThat"} {
		if !strings.Contains(src, want) {
			t.Errorf("smoke test does not exercise %s", want)
		}
	}

	again, err := writeJavaUnitSmokeTest(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.Wrote {
		t.Error("an existing file must never be clobbered")
	}
}

func TestRemoveJavaSmokeFile_onlyRemovesWhatThisRunWrote(t *testing.T) {
	root := t.TempDir()
	f, err := writeJavaUnitSmokeTest(root)
	if err != nil {
		t.Fatal(err)
	}
	preexisting := javaSmokeFile{Abs: f.Abs, FQCN: f.FQCN, Wrote: false}
	removeJavaSmokeFile(preexisting)
	if !fileExists(f.Abs) {
		t.Fatal("a file bootstrap did not create must survive removal")
	}
	removeJavaSmokeFile(f)
	if fileExists(f.Abs) {
		t.Fatal("a failed smoke test this run wrote must be removed so the evaluator never inherits it")
	}
}

func TestApplyGradleTestDeps_addsDepsAndUseJUnitPlatform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.gradle")
	if err := os.WriteFile(path, []byte("plugins { id 'java' }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof := buildJavaTestProfile(javaFrameworkDetection{Framework: JavaFrameworkPlain}, "17")

	changed, added, err := applyGradleTestDeps(path, false, prof)
	if err != nil || !changed {
		t.Fatalf("changed = %v err = %v", changed, err)
	}
	if len(added) == 0 {
		t.Fatal("expected added deps to be reported")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"junit-jupiter", "mockito-core", "assertj-core", "junit-platform-launcher", "useJUnitPlatform()"} {
		if !strings.Contains(s, want) {
			t.Errorf("build.gradle missing %s:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "testRuntimeOnly 'org.junit.platform:junit-platform-launcher") {
		t.Errorf("junit-platform-launcher must use testRuntimeOnly:\n%s", s)
	}

	changed2, _, err := applyGradleTestDeps(path, false, prof)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Error("second apply should be idempotent")
	}
}

func TestApplyGradleTestDeps_kotlinDSLSyntax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.gradle.kts")
	if err := os.WriteFile(path, []byte("plugins { id(\"java\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof := buildJavaTestProfile(javaFrameworkDetection{Framework: JavaFrameworkPlain}, "17")
	if _, _, err := applyGradleTestDeps(path, true, prof); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, `testImplementation("org.assertj:assertj-core:`) {
		t.Errorf("Kotlin DSL call syntax expected:\n%s", s)
	}
	if !strings.Contains(s, "tasks.withType<Test>()") {
		t.Errorf("Kotlin DSL useJUnitPlatform wiring expected:\n%s", s)
	}
}

func TestApplyMavenTestDeps_springBootOmitsVersionAndSurefire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pom.xml")
	if err := os.WriteFile(path, []byte(springBootParentPOM), 0o644); err != nil {
		t.Fatal(err)
	}
	prof := buildJavaTestProfile(detectJavaFrameworkMaven(springBootParentPOM), "17")

	changed, _, err := applyMavenTestDeps(path, prof)
	if err != nil || !changed {
		t.Fatalf("changed = %v err = %v", changed, err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "<artifactId>spring-boot-starter-test</artifactId>") {
		t.Fatalf("starter-test not added:\n%s", s)
	}
	// The starter parent manages the version; emitting one is how a Boot 3.2 app ends up on
	// Spring Test 6.0.
	idx := strings.Index(s, "spring-boot-starter-test")
	window := s[idx:min(idx+220, len(s))]
	if strings.Contains(window, "<version>") {
		t.Errorf("starter-test must not carry a version under the starter parent:\n%s", window)
	}
	if strings.Contains(s, "maven-surefire-plugin") {
		t.Error("the starter parent already configures Surefire")
	}
}
