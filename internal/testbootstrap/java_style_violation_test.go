package testbootstrap

import (
	"strings"
	"testing"
)

// springJavaformatFailure is the verbatim output from the run this classifier was written for:
// spring-petclinic validates spring-javaformat on every build, the bootstrap smoke test's javadoc
// wrapping violated it, and the whole run aborted at minute one.
const springJavaformatFailure = `[ERROR] Failed to execute goal io.spring.javaformat:spring-javaformat-maven-plugin:0.0.47:validate (default) on project spring-petclinic: Formatting violations found in the following files:
[ERROR]  * /workspace/src/test/java/com/asqs/bootstrap/AsqsBootstrapSmokeTest.java
[ERROR] 
[ERROR] Run ` + "`spring-javaformat:apply`" + ` to fix.
[ERROR] -> [Help 1]`

func TestIsStyleViolationFailure_recognisesFormatterRejections(t *testing.T) {
	if !isStyleViolationFailure(springJavaformatFailure) {
		t.Fatal("spring-javaformat rejection must be classified as style, not as a broken classpath")
	}
	for name, out := range map[string]string{
		"checkstyle":        "[ERROR] Failed to execute goal ... checkstyle:check: You have 3 Checkstyle violations.",
		"spotless":          "[ERROR] The following files had format violations. Run 'mvn spotless:apply' to fix these violations.",
		"gradle checkstyle": "Execution failed for task ':checkstyleMain'.\n> Checkstyle rule violations were found.",
	} {
		if !isStyleViolationFailure(out) {
			t.Errorf("%s should be classified as a style violation", name)
		}
	}
}

// TestIsStyleViolationFailure_neverMasksARealCompileError is the safety property: a build can fail
// for both reasons at once, and the compiler diagnostic must always win — that is the failure the
// gate exists to catch.
func TestIsStyleViolationFailure_neverMasksARealCompileError(t *testing.T) {
	realFailures := []string{
		"[ERROR] COMPILATION ERROR :\n[ERROR] /workspace/src/test/java/X.java:[5,19] package org.mockito does not exist",
		"[ERROR] /workspace/src/test/java/X.java:[47,19] cannot find symbol\n  symbol: method setId(long)",
		"[ERROR] /workspace/src/test/java/X.java:[19,2] <identifier> expected",
	}
	for _, out := range realFailures {
		if isStyleViolationFailure(out) {
			t.Errorf("a compiler diagnostic must never be downgraded:\n%s", out)
		}
	}
	// Both at once: the compile error wins.
	both := springJavaformatFailure + "\n[ERROR] /workspace/src/test/java/X.java:[5,19] package org.mockito does not exist"
	if isStyleViolationFailure(both) {
		t.Error("style + compile error together must not be downgraded")
	}
	if isStyleViolationFailure("") {
		t.Error("empty output is not a style violation")
	}
	if isStyleViolationFailure("[ERROR] Tests run: 1, Failures: 1") {
		t.Error("an ordinary test failure is not a style violation")
	}
}

func TestStyleViolationRemediation_namesTheTool(t *testing.T) {
	if got := styleViolationRemediation(springJavaformatFailure); !strings.Contains(got, "spring-javaformat") {
		t.Errorf("remediation should name the tool: %s", got)
	}
	if got := styleViolationRemediation("Checkstyle rule violations were found"); !strings.Contains(got, "Checkstyle") {
		t.Errorf("remediation should name the tool: %s", got)
	}
	if got := styleViolationRemediation(springJavaformatFailure); !strings.Contains(got, "format_command") {
		t.Errorf("remediation should point at the fix: %s", got)
	}
}

// TestMavenGoalsCarryLintSkips: the verification asks whether the classpath resolves, so the
// project's formatters — most of which bind to `validate`, before compile — must not gate it.
func TestMavenGoalsCarryLintSkips(t *testing.T) {
	r := javaGoalRunner{build: javaBuildPick{Kind: javaBuildMaven}}
	for _, goals := range [][]string{r.compileGoals(), r.singleTestGoals("com.asqs.bootstrap.AsqsBootstrapSmokeTest")} {
		joined := strings.Join(goals, " ")
		for _, want := range []string{"-Dspring-javaformat.skip=true", "-Dcheckstyle.skip=true", "-Dspotless.check.skip=true"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %s in %q", want, joined)
			}
		}
	}
	// Gradle has no equivalent -D convention; excluding tasks that do not exist is an error there,
	// so the classifier is the safety net instead.
	g := javaGoalRunner{build: javaBuildPick{Kind: javaBuildGradleGroovy}}
	if strings.Contains(strings.Join(g.compileGoals(), " "), "-D") {
		t.Error("Gradle goals must not carry Maven -D properties")
	}
}

func TestRemoveRelPath(t *testing.T) {
	in := []string{"pom.xml", "src/test/java/com/asqs/bootstrap/AsqsBootstrapSmokeTest.java"}
	got := removeRelPath(in, "src/test/java/com/asqs/bootstrap/AsqsBootstrapSmokeTest.java")
	if len(got) != 1 || got[0] != "pom.xml" {
		t.Fatalf("got %v", got)
	}
	if len(removeRelPath(in, "nope")) != 2 {
		t.Error("removing an absent path should be a no-op")
	}
}
