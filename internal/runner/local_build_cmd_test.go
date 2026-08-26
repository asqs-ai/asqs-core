package runner

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

func writeRepo(t *testing.T, files map[string]string, exec map[string]bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		mode := os.FileMode(0o644)
		if exec[name] {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func argvOf(t *testing.T, dir, goal, buildTool, compileCmd, testCmd string) []string {
	t.Helper()
	c, err := localBuildCommand(dir, goal, buildTool, compileCmd, testCmd)
	if err != nil {
		t.Fatalf("goal %s: %v", goal, err)
	}
	return c.Args
}

func TestLocalBuildCommand_MavenGoals(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	dir := writeRepo(t, map[string]string{"pom.xml": jacocoPom}, nil)
	for _, tc := range []struct {
		goal string
		want []string
	}{
		{"compile", []string{"mvn", "-q", "-B", "-DskipTests", "test-compile"}},
		{"test", []string{"mvn", "-q", "-B", "test"}},
		{"default", []string{"mvn", "-q", "-B", "test"}},
		{"coverage", []string{"mvn", "-q", "-B", "test", "jacoco:report"}},
	} {
		if got := argvOf(t, dir, tc.goal, "auto", "", ""); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("goal %s: argv = %v, want %v", tc.goal, got, tc.want)
		}
	}
}

func TestLocalBuildCommand_GradleGoals(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	dir := writeRepo(t, map[string]string{"build.gradle": "plugins { id 'jacoco' }"}, nil)
	for _, tc := range []struct {
		goal string
		want []string
	}{
		{"compile", []string{"gradle", "--no-daemon", "-q", "compileTestJava"}},
		{"test", []string{"gradle", "--no-daemon", "-q", "test"}},
		{"coverage", []string{"gradle", "--no-daemon", "-q", "test", "jacocoTestReport"}},
	} {
		if got := argvOf(t, dir, tc.goal, "auto", "", ""); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("goal %s: argv = %v, want %v", tc.goal, got, tc.want)
		}
	}
}

// Without the plugin, `mvn test jacoco:report` fails the whole step with "No plugin found for
// prefix 'jacoco'", and running plain `mvn test` again would only repeat the test step. Skip.
func TestLocalBuildCommand_CoverageSkippedWithoutJaCoCo(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	for name, files := range map[string]map[string]string{
		"maven":  {"pom.xml": "<project/>"},
		"gradle": {"build.gradle": "plugins { id 'java' }"},
	} {
		dir := writeRepo(t, files, nil)
		_, err := localBuildCommand(dir, "coverage", "auto", "", "")
		if !errors.Is(err, errLocalCoverageUnavailable) {
			t.Errorf("%s: err = %v, want errLocalCoverageUnavailable", name, err)
		}
		// compile and test must still work on the same repo.
		if got := argvOf(t, dir, "test", "auto", "", ""); len(got) == 0 {
			t.Errorf("%s: test goal broke", name)
		}
	}
}

// An explicit general.build.test_command still wins for the coverage step (it is what RunCoverage passes
// through CoverageWithCommand), so the JaCoCo gate never sees it.
func TestLocalBuildCommand_TestCommandOverridesCoverage(t *testing.T) {
	dir := writeRepo(t, map[string]string{"pom.xml": "<project/>"}, nil)
	got := argvOf(t, dir, "coverage", "auto", "", "mvn -q -B test jacoco:report")
	want := []string{"sh", "-c", "mvn -q -B test jacoco:report"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestLocalBuildCommand_SetsCIEnv(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	dir := writeRepo(t, map[string]string{"pom.xml": "<project/>"}, nil)
	for _, goal := range []string{"compile", "test"} {
		c, err := localBuildCommand(dir, goal, "auto", "", "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, kv := range c.Env {
			if kv == "CI=true" {
				found = true
			}
		}
		if !found {
			t.Errorf("goal %s: CI=true missing from env", goal)
		}
	}
}

// Guard against the local and docker coverage steps drifting apart again: docker has always run
// the JaCoCo report goal, local used to run a bare `test`.
func TestLocalCoverageMatchesDockerToolchainGoals(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	mvnDir := writeRepo(t, map[string]string{"pom.xml": jacocoPom}, nil)
	local := argvOf(t, mvnDir, "coverage", "mvn", "", "")
	docker := profile.BuiltinToolchain(profile.JavaMaven, "", "", "", "").Coverage
	// Sequence-exact since U1 converged local onto the docker flag order. A set comparison would
	// pass on a reordering that changes what Maven does (e.g. a goal read as a flag argument).
	if strings.Join(local, " ") != strings.Join(docker, " ") {
		t.Errorf("maven coverage: local %v vs docker %v", local, docker)
	}

	gradleDir := writeRepo(t,
		map[string]string{"build.gradle": "plugins { id 'jacoco' }", "gradlew": "#!/bin/sh\n"},
		map[string]bool{"gradlew": true})
	localG := argvOf(t, gradleDir, "coverage", "gradlew", "", "")
	dockerG := profile.BuiltinToolchain(profile.JavaGradle, "", "", "", "").Coverage
	if strings.Join(localG, " ") != strings.Join(dockerG, " ") {
		t.Errorf("gradle coverage: local %v vs docker %v", localG, dockerG)
	}
}

func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// general.sandbox.type: local has no image to supply a toolchain, so a missing build tool is a deployment
// mistake. Say so before spawning, naming the config keys, instead of surfacing a bare exec error
// after the fact.
func TestRequireLocalToolchain_MissingBuildToolNamesTheConfigKeys(t *testing.T) {
	stubToolsOnPATH(t) // empty stub dir: nothing extra, and mvn/gradle stay unavailable below
	t.Setenv("PATH", t.TempDir())
	dir := writeRepo(t, map[string]string{"pom.xml": "<project/>"}, nil)
	_, err := localBuildCommand(dir, "compile", "mvn", "", "")
	if err == nil {
		t.Fatal("expected an error when mvn is not on PATH")
	}
	for _, want := range []string{`"mvn" is not on PATH`, "runner.type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
