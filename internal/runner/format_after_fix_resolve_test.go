package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const spotlessPom = `<project><build><plugins><plugin>
  <groupId>io.spring.javaformat</groupId><artifactId>spring-javaformat-maven-plugin</artifactId>
</plugin></plugins></build></project>`

// Reproduces run api-9458e8be6512a02adb507bf3da91ce1e.
//
// A Java repo whose formatter is auto-detected, with runner.format_command empty. The run-scope
// step resolved `mvn spring-javaformat:apply -q` and applied it, but the AFTER-FIX path used a
// different resolver that only defaults for C#, so it produced "" and was never wired. The fixer's
// rewrite therefore went unformatted, the next compile failed on the formatter's own validate goal,
// and the fix loop spent its whole budget asking the LLM to hand-format Java.
func TestResolveFormatCommand_javaAutoDetectionWorksWithNoConfiguredCommand(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	repo := writeRepoTree(t, map[string]string{"pom.xml": spotlessPom}, nil)

	for _, target := range []Target{TargetLocal, TargetDocker} {
		got := ResolveFormatCommand(repo, "java", "", "auto", false, target)
		if got.Command == "" {
			t.Fatalf("%s: no formatter resolved for a repo that declares spring-javaformat (skip=%q)", target, got.SkipReason)
		}
		if !strings.Contains(got.Command, "spring-javaformat:apply") {
			t.Errorf("%s: command = %q, want the spring-javaformat apply goal", target, got.Command)
		}
		if got.PerFile {
			t.Errorf("%s: a Maven goal is not a per-file formatter", target)
		}
	}
}

// The sharp edge behind the config workaround: a configured Maven command with format_only_added
// used to be classified per-file, so a path was appended and Maven read it as a lifecycle phase.
func TestResolveFormatCommand_configuredBuildToolGoalIsNeverPerFile(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	repo := writeRepoTree(t, map[string]string{"pom.xml": spotlessPom}, nil)

	for _, cmd := range []string{
		"mvn spring-javaformat:apply -q",
		"gradle spotlessApply",
		"./mvnw spring-javaformat:apply",
		"mvnw.cmd spring-javaformat:apply",
	} {
		got := ResolveFormatCommand(repo, "java", cmd, "auto", true /* onlyAdded */, TargetLocal)
		if got.PerFile {
			t.Errorf("%q was classified per-file; appending a path makes it a lifecycle phase", cmd)
		}
	}
}

// A genuine per-file formatter still is one.
func TestFormatCommandIsPerFileCapable(t *testing.T) {
	perFile := []string{"google-java-format -i", "gofmt -w", "prettier --write", "dotnet format", "/usr/local/bin/google-java-format -i"}
	repoWide := []string{
		"mvn spring-javaformat:apply -q", "gradle spotlessApply", "./gradlew spotlessApply",
		"gradlew.bat spotlessApply", "mvn -q fmt:format",
		"prettier --write . && eslint --fix .", // shell operator: the path would land on eslint
		"",
	}
	for _, c := range perFile {
		if !formatCommandIsPerFileCapable(c) {
			t.Errorf("%q should be per-file capable", c)
		}
	}
	for _, c := range repoWide {
		if formatCommandIsPerFileCapable(c) {
			t.Errorf("%q should NOT be per-file capable", c)
		}
	}
}

// FormatAfterFixForSandbox executes the resolver's decision rather than re-deriving it. A Maven
// goal marked repo-wide must run once for the tree, never once per file.
func TestFormatAfterFixForSandbox_honoursTheResolvedPerFileDecision(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "invocations.log")
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> " + counter + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "mvn"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := writeRepoTree(t, map[string]string{"pom.xml": spotlessPom}, nil)
	sb := &Sandbox{Type: "local", Timeout: "30s"}
	resolved := FormatResolveResult{Command: "mvn spring-javaformat:apply -q", Source: "config", PerFile: false}

	if err := FormatAfterFixForSandbox(sb, context.Background(), repo, "java", resolved,
		[]string{"src/A.java", "src/B.java"}, 30*time.Second); err != nil {
		t.Fatalf("FormatAfterFixForSandbox: %v", err)
	}

	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("maven was never invoked: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("maven ran %d times, want once for the whole repository:\n%s", len(lines), b)
	}
	if strings.Contains(lines[0], ".java") {
		t.Errorf("a file path was appended to a repo-wide goal: %q", lines[0])
	}
}

// An unresolvable formatter is a no-op, not an error: it must not fail a fix.
func TestFormatAfterFixForSandbox_emptyCommandIsANoOp(t *testing.T) {
	sb := &Sandbox{Type: "local", Timeout: "30s"}
	if err := FormatAfterFixForSandbox(sb, context.Background(), t.TempDir(), "java",
		FormatResolveResult{Source: "none", SkipReason: "no java formatter detected in repo"},
		[]string{"src/A.java"}, 30*time.Second); err != nil {
		t.Fatalf("an unresolved formatter should be a no-op, got %v", err)
	}
}

// WithPerFile narrows the decision for one invocation without losing the rest of the result.
func TestFormatResolveResult_WithPerFile(t *testing.T) {
	base := FormatResolveResult{Command: "google-java-format -i", Source: "auto_google_java_format", PerFile: true}
	got := base.WithPerFile(false)
	if got.PerFile || got.Command != base.Command || got.Source != base.Source {
		t.Errorf("WithPerFile(false) = %+v, want only PerFile flipped", got)
	}
	if !base.PerFile {
		t.Error("WithPerFile must not mutate the receiver")
	}
}
