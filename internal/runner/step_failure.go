// Package runner: failure-result construction for eval steps, shared by both sandbox targets.
//
// This began as a local-only helper, on the reasoning that `docker run` always writes something
// to the combined output so the empty-output case could only happen on a host. That was half
// right. The Docker path had the same hole by a different route: on a timeout the docker CLI is
// SIGKILLed, so ProcessState.ExitCode() reports -1, and the old
// `runErr != nil && res.ExitCode == 0` gate in runDockerEvalWithImageOverride therefore skipped
// the error branch and discarded the deadline entirely — a killed container was reported as an
// ordinary test failure over whatever partial output had been flushed, and the fixer was sent to
// repair code that was fine.
//
// Both targets now route failures through sandboxStepFailure, so a timeout is named identically
// on each and errclass.KindStepTimeout classifies it on each.
package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// sandboxStepFailure builds the StepResult for a FAILED eval step, on either sandbox target.
//
// A step can fail before writing a single byte: on a host the binary is not on PATH or a build
// wrapper has no execute bit; under Docker the CLI itself failed to start or the JobSpec was
// rejected. On both, the step may have hit general.sandbox.timeout. The run error is then the only
// diagnostic that exists, so it becomes both Summary and Output. Without this the step reported a
// bare "compile failed" / "tests failed" with an empty Output — and because
// evaluator.compileErrorTouchesArtifactScope treats empty output as in-scope, the LLM fixer was
// invoked with nothing to work from and spent the whole iteration budget on what was really a
// host-environment problem.
//
// A timeout is named even when the command did print, because otherwise a killed build is
// indistinguishable from a genuine test failure and the fixer is asked to repair code that was
// fine. The note is deterministic, so the fix-loop circuit breakers that compare failure
// signatures across iterations still see a stable string.
// dockerJobRunError is sandboxStepFailure's counterpart for the error-returning Docker paths (the
// format step), which have no StepResult to fill. Same defect, same fix: without naming the
// deadline, a container killed at general.sandbox.timeout surfaced as `exit -1` — or, where the caller
// checked ExitCode first, as an ordinary non-zero exit.
func dockerJobRunError(what string, runErr error, out string, timeout time.Duration) error {
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return fmt.Errorf("%s: step timed out after %s (general.sandbox.timeout)\n%s", what, timeout, out)
	case errors.Is(runErr, context.Canceled):
		return fmt.Errorf("%s: cancelled before it completed\n%s", what, out)
	default:
		return fmt.Errorf("%s: %w\n%s", what, runErr, out)
	}
}

func sandboxStepFailure(step evaluator.SandboxStep, out string, runErr error, timeout time.Duration) evaluator.StepResult {
	summary := failedStepSummary(step, out, 5)
	hasOutput := strings.TrimSpace(out) != ""
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		note := fmt.Sprintf("%s step timed out after %s (general.sandbox.timeout)", step, timeout)
		if hasOutput {
			summary = note + "\n" + summary
		} else {
			summary, out = note, note
		}
	case errors.Is(runErr, context.Canceled):
		// Same class as the timeout: the step was killed rather than failing on its own merits,
		// so partial output alone would read as a genuine test failure. Deliberately worded
		// without "timed out" so errclass does not classify a cancelled run as KindStepTimeout.
		note := fmt.Sprintf("%s step was cancelled before it completed", step)
		if hasOutput {
			summary = note + "\n" + summary
		} else {
			summary, out = note, note
		}
	case !hasOutput && runErr != nil:
		// e.g. exec: "mvn": executable file not found in $PATH / fork/exec ./mvnw: permission denied
		summary = runErr.Error()
		out = summary
	}
	return evaluator.StepResult{Step: step, OK: false, Summary: summary, Output: out}
}
