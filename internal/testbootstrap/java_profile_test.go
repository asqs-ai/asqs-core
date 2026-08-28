package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const springBootParentPOM = `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.2.5</version>
  </parent>
  <groupId>com.example</groupId>
  <artifactId>java-test</artifactId>
  <properties><java.version>17</java.version></properties>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
    </dependency>
  </dependencies>
</project>`

func TestDetectJavaFrameworkMaven(t *testing.T) {
	cases := []struct {
		name        string
		pom         string
		wantFW      JavaFramework
		wantVersion string
		wantManaged bool
	}{
		{
			name:        "spring boot starter parent",
			pom:         springBootParentPOM,
			wantFW:      JavaFrameworkSpringBoot,
			wantVersion: "3.2.5",
			wantManaged: true,
		},
		{
			name: "spring boot dependencies BOM without parent",
			pom: `<project><dependencyManagement><dependencies><dependency>
			        <groupId>org.springframework.boot</groupId>
			        <artifactId>spring-boot-dependencies</artifactId>
			        <version>3.3.0</version><type>pom</type><scope>import</scope>
			      </dependency></dependencies></dependencyManagement></project>`,
			wantFW:      JavaFrameworkSpringBoot,
			wantVersion: "3.3.0",
			wantManaged: true,
		},
		{
			name: "spring boot via property only is not version managed",
			pom: `<project><properties><spring-boot.version>3.1.0</spring-boot.version></properties>
			      <dependencies><dependency><groupId>org.springframework</groupId>
			      <artifactId>spring-web</artifactId></dependency></dependencies></project>`,
			wantFW:      JavaFrameworkSpringBoot,
			wantVersion: "3.1.0",
			wantManaged: false,
		},
		{
			name: "quarkus",
			pom: `<project><properties><quarkus.platform.version>3.15.1</quarkus.platform.version></properties>
			      <dependencyManagement><dependencies><dependency><groupId>io.quarkus.platform</groupId>
			      <artifactId>quarkus-bom</artifactId></dependency></dependencies></dependencyManagement></project>`,
			wantFW:      JavaFrameworkQuarkus,
			wantVersion: "3.15.1",
			wantManaged: true,
		},
		{
			name: "micronaut",
			pom: `<project><parent><groupId>io.micronaut.platform</groupId>
			      <artifactId>micronaut-parent</artifactId><version>4.6.3</version></parent></project>`,
			wantFW:      JavaFrameworkMicronaut,
			wantVersion: "4.6.3",
			wantManaged: true,
		},
		{
			name:   "plain",
			pom:    `<project><groupId>com.example</groupId><artifactId>bare</artifactId></project>`,
			wantFW: JavaFrameworkPlain,
		},
		{
			name: "commented-out spring boot parent is not a detection",
			pom: `<project><!-- <parent><artifactId>spring-boot-starter-parent</artifactId>
			      <version>3.2.5</version></parent> --><artifactId>bare</artifactId></project>`,
			wantFW: JavaFrameworkPlain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectJavaFrameworkMaven(tc.pom)
			if got.Framework != tc.wantFW {
				t.Fatalf("framework = %q, want %q (evidence: %s)", got.Framework, tc.wantFW, got.Evidence)
			}
			if tc.wantVersion != "" && got.Version != tc.wantVersion {
				t.Errorf("version = %q, want %q", got.Version, tc.wantVersion)
			}
			if got.VersionManaged != tc.wantManaged {
				t.Errorf("VersionManaged = %v, want %v", got.VersionManaged, tc.wantManaged)
			}
		})
	}
}

func TestDetectJavaFrameworkGradle(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		wantFW      JavaFramework
		wantManaged bool
	}{
		{
			name:        "spring boot with dependency management",
			src:         `plugins { id 'org.springframework.boot' version '3.2.5'; id 'io.spring.dependency-management' version '1.1.4' }`,
			wantFW:      JavaFrameworkSpringBoot,
			wantManaged: true,
		},
		{
			name:        "spring boot plugin alone does not manage versions",
			src:         `plugins { id("org.springframework.boot") version "3.2.5" }`,
			wantFW:      JavaFrameworkSpringBoot,
			wantManaged: false,
		},
		{
			name:   "android is detected so bootstrap can decline",
			src:    `plugins { id 'com.android.application' }`,
			wantFW: JavaFrameworkAndroid,
		},
		{
			name:        "micronaut",
			src:         `plugins { id("io.micronaut.application") version "4.4.2" }`,
			wantFW:      JavaFrameworkMicronaut,
			wantManaged: true,
		},
		{
			name:   "plain",
			src:    `plugins { id 'java' }`,
			wantFW: JavaFrameworkPlain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectJavaFrameworkGradle(tc.src)
			if got.Framework != tc.wantFW {
				t.Fatalf("framework = %q, want %q", got.Framework, tc.wantFW)
			}
			if got.VersionManaged != tc.wantManaged {
				t.Errorf("VersionManaged = %v, want %v", got.VersionManaged, tc.wantManaged)
			}
		})
	}
}

func TestBuildJavaTestProfile_springBootUsesStarterTestWithoutVersion(t *testing.T) {
	det := detectJavaFrameworkMaven(springBootParentPOM)
	p := buildJavaTestProfile(det, "17")

	if len(p.Deps) != 1 {
		t.Fatalf("want exactly spring-boot-starter-test, got %v", describeJavaDeps(p.Deps))
	}
	d := p.Deps[0]
	if d.ArtifactID != "spring-boot-starter-test" {
		t.Fatalf("dep = %s", d.coord())
	}
	if d.Version != "" {
		t.Errorf("version = %q; the starter parent must supply it, pinning risks a framework/test major mismatch", d.Version)
	}
	if p.NeedsSurefirePlugin {
		t.Error("spring-boot-starter-parent already configures Surefire; injecting another is churn")
	}
	if p.FrameworkSmoke != javaSmokeSpringBoot || !p.FrameworkSmokeRequired {
		t.Errorf("Spring Boot smoke must be required: %+v", p)
	}
}

func TestBuildJavaTestProfile_plainIncludesMockitoAndAssertJ(t *testing.T) {
	p := buildJavaTestProfile(javaFrameworkDetection{Framework: JavaFrameworkPlain}, "17")
	got := strings.Join(describeJavaDeps(p.Deps), " ")
	// Generation reaches for these on plain modules too: the motivating run had 20 candidates
	// rejected for referencing org.mockito and org.assertj on a module that carried neither.
	for _, want := range []string{"junit-jupiter", "mockito-core", "mockito-junit-jupiter", "assertj-core"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain profile missing %s; got %s", want, got)
		}
	}
	if p.FrameworkSmoke != javaSmokeNone {
		t.Error("plain modules have no application context to boot")
	}
}

func TestBuildJavaTestProfile_androidIsDeclined(t *testing.T) {
	p := buildJavaTestProfile(javaFrameworkDetection{Framework: JavaFrameworkAndroid}, "17")
	if !p.Declined || p.DeclinedReason == "" {
		t.Fatalf("Android must be declined with a reason: %+v", p)
	}
	if len(p.Deps) != 0 {
		t.Error("a declined profile must not propose dependencies")
	}
}

func TestJavaMockitoVersion_respectsLanguageLevel(t *testing.T) {
	// Mockito 5 needs Java 11; on Java 8 it dies at class-load with UnsupportedClassVersionError.
	if got := javaMockitoVersion("1.8"); got != VersionMockitoJava8 {
		t.Errorf("java 8 → %q, want %q", got, VersionMockitoJava8)
	}
	if got := javaMockitoVersion("8"); got != VersionMockitoJava8 {
		t.Errorf("java 8 → %q, want %q", got, VersionMockitoJava8)
	}
	if got := javaMockitoVersion("17"); got != VersionMockito {
		t.Errorf("java 17 → %q, want %q", got, VersionMockito)
	}
	if got := javaMockitoVersion(""); got != VersionMockito {
		t.Errorf("unknown → %q, want the modern line %q", got, VersionMockito)
	}
}

func TestMissingDeps_partialStackIsIncomplete(t *testing.T) {
	p := buildJavaTestProfile(javaFrameworkDetection{Framework: JavaFrameworkPlain}, "17")
	pom := `<project><dependencies><dependency><groupId>org.junit.jupiter</groupId>
	        <artifactId>junit-jupiter</artifactId></dependency></dependencies></project>`
	missing := p.missingDeps(pom, true)
	if len(missing) == 0 {
		t.Fatal("a module with only junit-jupiter is NOT fully equipped")
	}
	for _, d := range missing {
		if d.ArtifactID == "junit-jupiter" {
			t.Error("junit-jupiter is present and must not be reported missing")
		}
	}
}

// TestDetectJava_springBootWithBareJUnitNeedsBootstrap is the regression test for the run this work
// came from: a Spring Boot pom carrying junit-jupiter + surefire and nothing else passed the old
// substring detection, skipped bootstrap, and produced 102 compile errors across 6 generated files.
func TestDetectJava_springBootWithBareJUnitNeedsBootstrap(t *testing.T) {
	dir := t.TempDir()
	pom := strings.Replace(springBootParentPOM, "</dependencies>", `
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.11.3</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
  <build><plugins><plugin><artifactId>maven-surefire-plugin</artifactId></plugin></plugins></build>`, 1)
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Detect(dir, "java")
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasFramework {
		t.Fatalf("Spring Boot module with only junit-jupiter must still be bootstrapped; got skip: %s", rep.Reason)
	}
	if !strings.Contains(rep.Reason, "spring-boot-starter-test") {
		t.Errorf("reason should name the missing coordinate, got: %s", rep.Reason)
	}
}

func TestDetectJava_fullyEquippedSpringBootIsSkipped(t *testing.T) {
	dir := t.TempDir()
	pom := strings.Replace(springBootParentPOM, "</dependencies>", `
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-test</artifactId>
      <scope>test</scope>
    </dependency>
  </dependencies>`, 1)
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Detect(dir, "java")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasFramework {
		t.Fatalf("a module with spring-boot-starter-test needs nothing: %s", rep.Reason)
	}
}
