package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// A host command can die before writing a byte (binary missing, wrapper not executable). The exec
// error is then the only diagnostic there is, and dropping it left the evaluator's fixer with an
// empty compile error to "repair".
func TestLocalStepFailure_EmptyOutputKeepsExecError(t *testing.T) {
	runErr := errors.New(`exec: "mvn": executable file not found in $PATH`)
	res := sandboxStepFailure(evaluator.StepCompile, "", runErr, 15*time.Minute)
	if res.OK {
		t.Fatal("expected OK=false")
	}
	if res.Summary != runErr.Error() {
		t.Errorf("Summary = %q, want the exec error", res.Summary)
	}
	if res.Output != runErr.Error() {
		t.Errorf("Output = %q, want the exec error (empty Output reads as in-scope to the fixer)", res.Output)
	}
}

func TestLocalStepFailure_TimeoutIsNamed(t *testing.T) {
	res := sandboxStepFailure(evaluator.StepTest, "", context.DeadlineExceeded, 30*time.Minute)
	if !strings.Contains(res.Summary, "timed out after 30m0s") {
		t.Errorf("Summary = %q, want the timeout named", res.Summary)
	}
	if res.Output != res.Summary {
		t.Errorf("Output = %q, want it to carry the timeout too", res.Output)
	}
}

// A killed build usually printed something first. Without the note the partial log reads as an
// ordinary test failure and the fixer is asked to repair code that was fine.
func TestLocalStepFailure_TimeoutWithPartialOutput(t *testing.T) {
	out := "[INFO] Scanning for projects\n[INFO] Building demo 1.0\n"
	res := sandboxStepFailure(evaluator.StepTest, out, context.DeadlineExceeded, time.Minute)
	if !strings.HasPrefix(res.Summary, "test step timed out after 1m0s") {
		t.Errorf("Summary = %q, want the timeout note first", res.Summary)
	}
	if !strings.Contains(res.Summary, "Scanning for projects") {
		t.Errorf("Summary = %q, want the partial log kept", res.Summary)
	}
	if res.Output != out {
		t.Errorf("Output = %q, want the raw output untouched", res.Output)
	}
}

// An ordinary failing build keeps the previous summary behaviour.
func TestLocalStepFailure_OrdinaryFailureUsesStepSummary(t *testing.T) {
	out := "line1\nline2\nline3\nline4\nline5\nline6\n"
	res := sandboxStepFailure(evaluator.StepCompile, out, errors.New("exit status 1"), time.Minute)
	if want := failedStepSummary(evaluator.StepCompile, out, 5); res.Summary != want {
		t.Errorf("Summary = %q, want %q", res.Summary, want)
	}
	if res.Output != out {
		t.Errorf("Output = %q, want the raw output", res.Output)
	}
}
