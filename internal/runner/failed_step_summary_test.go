package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// The failed test-step summary must show the failures, not the log's head: run
// api-12aa1935d113c9ea8b50a516fd275660's evaluator.test audit rows carried only Spring
// context-loader INFO lines across twelve failing iterations, and the post-mortem had to dig the
// real failures out of surefire XML files.
func TestFailedStepSummary_testStepShowsFailuresNotNoise(t *testing.T) {
	out := strings.Join([]string{
		"12:00:00.001 [main] INFO org.springframework.test.context.support.AnnotationConfigContextLoaderUtils -- Could not detect default configuration classes",
		"12:00:00.002 [main] INFO org.springframework.boot.test.context.SpringBootTestContextBootstrapper -- Found @SpringBootConfiguration",
		"12:00:00.003 [main] INFO org.springframework.boot.devtools.restart.RestartApplicationListener -- Restart disabled",
		"[ERROR] addVisit_WhenPetExists_AddsVisitToPet  Time elapsed: 0.01 s  <<< ERROR!",
		"org.mockito.exceptions.misusing.MissingMethodInvocationException:",
		"at org.springframework.samples.petclinic.owner.OwnerTests.addVisit_WhenPetExists_AddsVisitToPet(OwnerTests.java:171)",
	}, "\n")
	got := failedStepSummary(evaluator.StepTest, out, 5)
	if !strings.Contains(got, "MissingMethodInvocationException") || !strings.Contains(got, "OwnerTests.java:171") {
		t.Fatalf("summary lost the failure:\n%s", got)
	}
	if strings.Contains(got, "Could not detect default configuration classes") {
		t.Fatalf("summary still leads with framework noise:\n%s", got)
	}
}

// A test log with no recognisable failure marker keeps the old head behaviour byte-for-byte, and
// so does every non-test step.
func TestFailedStepSummary_fallbacks(t *testing.T) {
	noise := "line one\nline two\nline three\n"
	if got, want := failedStepSummary(evaluator.StepTest, noise, 2), firstLines(noise, 2); got != want {
		t.Errorf("markerless test log: got %q, want plain head %q", got, want)
	}
	compileOut := "[ERROR] COMPILATION ERROR :\nsomething broke"
	if got, want := failedStepSummary(evaluator.StepCompile, compileOut, 2), firstLines(compileOut, 2); got != want {
		t.Errorf("compile step must keep the head: got %q, want %q", got, want)
	}
	if got := failedStepSummary(evaluator.StepTest, "", 5); got != "failed" {
		t.Errorf("empty output: got %q, want \"failed\"", got)
	}
}

// The E2E pass routes through the docker eval with the test-step id, but the helper must also
// answer StepTestE2E for the local/E2E-specific paths.
func TestFailedStepSummary_e2eStepUsesExcerpt(t *testing.T) {
	out := "noise\nFAIL src/e2e/login.spec.ts\nexpected: 200 but was: 500\n"
	got := failedStepSummary(evaluator.StepTestE2E, out, 1)
	if !strings.Contains(got, "login.spec.ts") {
		t.Fatalf("e2e excerpt lost:\n%s", got)
	}
}
