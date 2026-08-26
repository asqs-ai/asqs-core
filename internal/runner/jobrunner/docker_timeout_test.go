package jobrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDockerBinary writes an executable stand-in for the docker CLI and returns its path.
//
// `exec` in the script matters: it replaces the shell with the named process, so the job has
// exactly one child. Without it a killed shell can leave a grandchild holding the output pipe
// open and CombinedOutput blocks forever, which would make these tests hang rather than fail.
func fakeDockerBinary(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalSpec(t *testing.T, dockerBin string, timeout time.Duration) JobSpec {
	t.Helper()
	return JobSpec{
		Image:        "example:latest",
		HostWorkDir:  t.TempDir(),
		Workdir:      "/workspace",
		Command:      []string{"true"},
		Timeout:      timeout,
		DockerBinary: dockerBin,
	}
}

// The premise the eval-side timeout fix rests on: when the deadline fires, the docker CLI is
// SIGKILLed, so ProcessState.ExitCode() reports -1 — NOT 0. Any caller gating its error branch on
// `ExitCode == 0` therefore drops the deadline silently. If Go's exec ever changes this, the
// runner fix needs revisiting, so pin it here rather than in a comment.
func TestDockerRunner_timeoutReportsNegativeExitCodeAndDeadlineError(t *testing.T) {
	bin := fakeDockerBinary(t, `echo "partial output from container"
exec sleep 30`)

	start := time.Now()
	res, err := (&DockerRunner{Docker: bin}).Run(context.Background(), minimalSpec(t, bin, 150*time.Millisecond))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the job exceeds its timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must unwrap to context.DeadlineExceeded for errors.Is to work in sandboxStepFailure; got %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("ExitCode was 0 on a killed CLI; the eval gate depends on it being non-zero (got %d)", res.ExitCode)
	}
	if res.ExitCode != -1 {
		t.Logf("note: ExitCode was %d, not the expected -1; the fix does not depend on the exact value", res.ExitCode)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("run did not return promptly after the deadline (%s) — a grandchild may be holding the output pipe", elapsed)
	}
	// Output written before the kill does NOT survive: exec.CommandContext discards the buffered
	// combined output when it kills the process (verified — the container's `echo` above is gone).
	//
	// This is why the discarded-deadline bug was worse than "a timeout looks like a test failure".
	// With no output AND a non-zero ExitCode, the old gate fell through to
	// failedStepSummary(step, "", 5), which returns the bare string "failed" with an empty Output —
	// and evaluator.compileErrorTouchesArtifactScope treats empty output as in-scope, so the LLM
	// fixer was handed nothing and burned the iteration budget. Exactly the F1 pathology that
	// sandboxStepFailure was written to prevent on the local path.
	if strings.TrimSpace(res.CombinedOutput) != "" {
		t.Errorf("assumption changed: killed job now yields output %q; revisit the empty-output "+
			"reasoning in step_failure.go", res.CombinedOutput)
	}
}

// An ordinary non-zero exit is NOT a run error: jobrunner reports it as (res, nil) through its
// ExitError branch, and the caller distinguishes a failed build from a failed job on that basis.
func TestDockerRunner_ordinaryNonZeroExitReturnsNilError(t *testing.T) {
	bin := fakeDockerBinary(t, `echo "tests failed"
exit 3`)

	res, err := (&DockerRunner{Docker: bin}).Run(context.Background(), minimalSpec(t, bin, 30*time.Second))
	if err != nil {
		t.Fatalf("a non-zero container exit must not be reported as a run error, got %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.CombinedOutput, "tests failed") {
		t.Errorf("output not captured: %q", res.CombinedOutput)
	}
}

// When the CLI cannot start at all, exec never produces a ProcessState, so ExitCode stays 0 and
// the error is the only signal. This is the case the old `ExitCode == 0` gate did handle; keep it.
func TestDockerRunner_missingBinaryReportsErrorWithZeroExitCode(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "docker-does-not-exist")

	res, err := (&DockerRunner{Docker: missing}).Run(context.Background(), minimalSpec(t, missing, 30*time.Second))
	if err == nil {
		t.Fatal("expected an error when the docker binary does not exist")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("a missing binary must not be reported as a timeout")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (no process ever ran)", res.ExitCode)
	}
}
