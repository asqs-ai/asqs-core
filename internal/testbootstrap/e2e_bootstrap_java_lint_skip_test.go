package testbootstrap

import (
	"strings"
	"testing"
)

// The Playwright-for-Java verification must run Maven with the same lint-skip properties as the
// unit bootstrap: a formatter bound to `validate` otherwise aborts the build before test-compile
// asks whether the classpath resolves (spring-petclinic, spring-javaformat, run of 2026-09-04).
func TestMavenBootstrapArgs_carryLintSkipProps(t *testing.T) {
	got := mavenBootstrapArgs("test-compile")
	if len(got) == 0 || got[0] != "test-compile" {
		t.Fatalf("goal must come first: %v", got)
	}
	joined := " " + strings.Join(got, " ") + " "
	for _, p := range mavenLintSkipProps {
		if !strings.Contains(joined, " "+p+" ") {
			t.Fatalf("missing %s in %v", p, got)
		}
	}
	if !strings.Contains(joined, " -Dspring-javaformat.skip=true ") {
		t.Fatalf("spring-javaformat must be skipped: %v", got)
	}
	// The input slice is not aliased: appending to the result must not touch the caller's goals.
	goals := []string{"test", "-Dtest=X"}
	out := mavenBootstrapArgs(goals...)
	out[0] = "changed"
	if goals[0] != "test" {
		t.Fatal("mavenBootstrapArgs must copy its input")
	}
}

// The embedded Java stub class is written verbatim into a repository that may run
// spring-javaformat:validate on every build, so it has to be the formatter's own output: tabs for
// code indentation, no line over 120 columns, no trailing whitespace, and the ownership marker the
// writer looks for. (The 2026-09-04 template wrapped javadoc at 100 columns and chained calls the
// way the formatter does not; validate rejected it.)
func TestJavaAPIStubsTemplate_isSpringJavaformatShaped(t *testing.T) {
	if !strings.Contains(javaAPIStubsClass, asqsE2EGeneratedHeader) {
		t.Fatal("template lost the ownership marker")
	}
	for i, line := range strings.Split(javaAPIStubsClass, "\n") {
		if strings.HasPrefix(line, " ") && !strings.HasPrefix(strings.TrimLeft(line, " "), "*") {
			t.Fatalf("line %d indents with spaces (spring-javaformat wants tabs): %q", i+1, line)
		}
		if len(line) > 120 {
			t.Fatalf("line %d is %d columns; the formatter wraps at 120", i+1, len(line))
		}
		if strings.TrimRight(line, " \t") != line {
			t.Fatalf("line %d has trailing whitespace", i+1)
		}
	}
	for _, want := range []string{"public static void stubJson(", "stubJsonAfter(", "stubError("} {
		if !strings.Contains(javaAPIStubsClass, want) {
			t.Fatalf("template lost helper %q", want)
		}
	}
}
