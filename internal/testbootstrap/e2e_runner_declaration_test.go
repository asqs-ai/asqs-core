package testbootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asqs-go run api-0e0b356b780481deb471faa745dab0bc: the E2E bootstrap was skipped because Playwright
// was already in the POM, so the Failsafe declaration never landed. The skip path must still
// declare the runner the evaluator invokes, once.
func TestEnsureJavaE2ERunnerDeclared_maven(t *testing.T) {
	repo := t.TempDir()
	pom := filepath.Join(repo, "pom.xml")
	if err := os.WriteFile(pom, []byte(strings.Replace(minimalPom, "</dependencies>", mavenPlaywrightDep+"\n  </dependencies>", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureJavaE2ERunnerDeclared(context.Background(), nil, repo)
	b, _ := os.ReadFile(pom)
	if !strings.Contains(string(b), "maven-failsafe-plugin") || !strings.Contains(string(b), "<include>**/*E2EIT.java</include>") {
		t.Fatalf("Failsafe not declared:\n%s", b)
	}
	before := string(b)
	ensureJavaE2ERunnerDeclared(context.Background(), nil, repo)
	after, _ := os.ReadFile(pom)
	if string(after) != before {
		t.Fatal("second call must be a no-op")
	}
}

func TestEnsureGradleIntegrationTestTask(t *testing.T) {
	for _, tc := range []struct {
		name, file, body string
		kotlin           bool
		wantPlatform     bool
	}{
		{"groovy junit5", "build.gradle", "plugins { id 'java' }\ndependencies { testImplementation 'org.junit.jupiter:junit-jupiter:5.11.3' }\ntest { useJUnitPlatform() }\n", false, true},
		{"groovy junit4", "build.gradle", "plugins { id 'java' }\ndependencies { testImplementation 'junit:junit:4.13.2' }\n", false, false},
		{"kotlin junit5", "build.gradle.kts", "plugins { java }\ndependencies { testImplementation(\"org.junit.jupiter:junit-jupiter:5.11.3\") }\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, err := ensureGradleIntegrationTestTask(path, tc.kotlin)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			b, _ := os.ReadFile(path)
			s := string(b)
			for _, want := range []string{"integrationTest", "**/*IT.class"} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q:\n%s", want, s)
				}
			}
			body := s[strings.Index(s, "integrationTest"):]
			if tc.wantPlatform && !strings.Contains(body, "useJUnitPlatform()") {
				t.Errorf("JUnit platform line expected in the task:\n%s", s)
			}
			if !tc.wantPlatform && strings.Contains(body, "useJUnitPlatform") {
				t.Errorf("JUnit platform line must not be added to a JUnit 4 build:\n%s", s)
			}
			if tc.kotlin && !strings.Contains(s, `tasks.register<Test>("integrationTest")`) {
				t.Errorf("Kotlin DSL expected:\n%s", s)
			}
			if changed, _ := ensureGradleIntegrationTestTask(path, tc.kotlin); changed {
				t.Error("second call must be a no-op")
			}
		})
	}
}
