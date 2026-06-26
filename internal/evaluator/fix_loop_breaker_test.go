package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fixLoopErrorMagnitude is the coarse "how broken is the build" proxy backing the no-progress breaker.
func TestFixLoopErrorMagnitude_countsDistinctDiagnosticLines(t *testing.T) {
	if got := fixLoopErrorMagnitude(""); got != 0 {
		t.Errorf("empty => %d, want 0", got)
	}
	if got := fixLoopErrorMagnitude("all good\nBUILD SUCCESS"); got != 0 {
		t.Errorf("no diagnostics => %d, want 0", got)
	}
	// "[ERROR] cannot find symbol" appears twice (de-duplicated to 1) + "[ERROR] BUILD FAILURE" => 2.
	out := "[ERROR] cannot find symbol\n  symbol: class Pet\n[ERROR] cannot find symbol\n[ERROR] BUILD FAILURE"
	if got := fixLoopErrorMagnitude(out); got != 2 {
		t.Errorf("magnitude(out) = %d, want 2", got)
	}
	// Fewer diagnostics must yield a strictly smaller magnitude (so a converging fixer resets the streak).
	if fixLoopErrorMagnitude("[ERROR] BUILD FAILURE") >= fixLoopErrorMagnitude(out) {
		t.Errorf("expected fewer-error output to have smaller magnitude")
	}
}

// driveBreaker calls applyLLMFix `calls` times against a throwaway repo, feeding a per-iteration error
// string. The stub fixer applies nothing, isolating the circuit-breaker accounting from quality gates.
func driveBreaker(t *testing.T, calls int, errFor func(i int) string) (*recordingAuditor, *FixLoopState) {
	t.Helper()
	dir := t.TempDir()
	artifact := "src/test/java/FooTest.java"
	full := filepath.Join(dir, artifact)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("class FooTest {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.ArtifactPaths = []string{artifact}
	opts.Fixer = &stubFixer{resp: FixResponse{Files: nil}}
	audit := &recordingAuditor{}
	ls := &FixLoopState{}
	attempt := 0
	for i := 0; i < calls; i++ {
		applyLLMFix(context.Background(), opts, StepTest, errFor(i), audit, &attempt, 30, ls, "")
	}
	return audit, ls
}

func assertTripReason(t *testing.T, audit *recordingAuditor, ls *FixLoopState, wantReason string) {
	t.Helper()
	if !ls.tripped {
		t.Fatalf("breaker did not trip; want reason %q", wantReason)
	}
	p := audit.lastPayload("evaluator.fix_rejected_low_value")
	if p == nil {
		t.Fatalf("no evaluator.fix_rejected_low_value audit event recorded")
	}
	if got, _ := p["reason"].(string); got != wantReason {
		t.Fatalf("trip reason = %q; want %q", got, wantReason)
	}
}

// (a) Consecutive identical signature trips the original repeat breaker on the 3rd attempt.
func TestApplyLLMFix_breaker_consecutiveRepeat(t *testing.T) {
	const same = "[ERROR] cannot find symbol: class Pet\n[ERROR] BUILD FAILURE"
	audit, ls := driveBreaker(t, 3, func(int) string { return same })
	assertTripReason(t, audit, ls, "fix_loop_repeat")
}

// (b) A 2-state oscillation (A,B,A,B…) never repeats consecutively, but trips the recurrence breaker.
func TestApplyLLMFix_breaker_oscillation(t *testing.T) {
	a := "[ERROR] cannot find symbol: variable DOG\n[ERROR] BUILD FAILURE"
	b := "[ERROR] cannot find symbol: method valueOf\n[ERROR] BUILD FAILURE"
	audit, ls := driveBreaker(t, 4, func(i int) string {
		if i%2 == 0 {
			return a
		}
		return b
	})
	assertTripReason(t, audit, ls, "fix_loop_oscillation")
	if ls.recurrences < FixLoopRecurrenceStopThreshold {
		t.Errorf("recurrences = %d; want >= %d", ls.recurrences, FixLoopRecurrenceStopThreshold)
	}
}

// (c) The moving-target case: every attempt yields a *different* error (unique signature, never
// repeated, never oscillating) but the error magnitude never improves — the no-progress backstop fires.
// This is the exact PetClinic failure mode that previously burned all 30 iterations.
func TestApplyLLMFix_breaker_noProgressMovingTarget(t *testing.T) {
	audit, ls := driveBreaker(t, FixLoopNoProgressStopThreshold+1, func(i int) string {
		// Distinct type name each iteration => unique signature; identical magnitude (2) every time.
		return fmt.Sprintf("[ERROR] cannot find symbol: class Type%d\n[ERROR] BUILD FAILURE", i)
	})
	assertTripReason(t, audit, ls, "fix_loop_no_progress")
}

// A genuinely converging fixer (error magnitude strictly decreasing) must NOT trip the breaker.
func TestApplyLLMFix_breaker_doesNotTripOnProgress(t *testing.T) {
	// 6 attempts, each with one fewer distinct error line than the last.
	_, ls := driveBreaker(t, 6, func(i int) string {
		n := 6 - i // 6,5,4,...,1 distinct diagnostics
		s := ""
		for k := 0; k < n; k++ {
			s += fmt.Sprintf("[ERROR] cannot find symbol: class T%d_%d\n", i, k)
		}
		return s + "[ERROR] BUILD FAILURE"
	})
	if ls.tripped {
		t.Fatalf("breaker tripped on a converging run (no_progress_streak=%d, recurrences=%d)", ls.noProgressStreak, ls.recurrences)
	}
}
