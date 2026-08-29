package evaluator

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The breaker that fires must be the one the audit names, and the threshold reported must be the
// one the operator can change.
//
// All three used to report the repeat threshold, so an oscillation trip read as
// self-contradictory: "reappeared 2 time(s)" beside "threshold: 3".
func TestCheckFixLoopBreakers_reportsTheBreakerThatActuallyFired(t *testing.T) {
	for _, tc := range []struct {
		name          string
		prime         func(*FixLoopState)
		opts          EvalOptions
		wantReason    string
		wantThreshold int
	}{
		{
			name:          "repeat",
			prime:         func(s *FixLoopState) { s.lastSignature = "sig"; s.streak = 2; s.seen = map[string]int{"sig": 2} },
			wantReason:    FixSkipLoopRepeat,
			wantThreshold: FixLoopRepeatStopThreshold,
		},
		{
			name: "oscillation",
			prime: func(s *FixLoopState) {
				s.lastSignature = "other"
				s.recurrences = FixLoopRecurrenceStopThreshold - 1
				s.seen = map[string]int{"sig": 1}
			},
			wantReason:    FixSkipLoopOscillation,
			wantThreshold: FixLoopRecurrenceStopThreshold,
		},
		{
			name: "no progress",
			prime: func(s *FixLoopState) {
				s.magnitudeKnown = true
				s.bestMagnitude = 0
				s.noProgressStreak = FixLoopNoProgressStopThreshold - 1
			},
			wantReason:    FixSkipLoopNoProgress,
			wantThreshold: FixLoopNoProgressStopThreshold,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &FixLoopState{}
			tc.prime(state)
			audit := &recordingAuditor{}
			counter := 0
			stop, reason := checkFixLoopBreakers(context.Background(), tc.opts, StepCompile,
				"[ERROR] boom\n", "sig", []string{"a/BTest.java"}, false, audit, &counter, 9, state)

			if !stop {
				t.Fatalf("the %s breaker should have tripped", tc.name)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q — the caller's label must name the breaker that fired", reason, tc.wantReason)
			}
			p := audit.lastPayload("evaluator.fix_rejected_low_value")
			if p == nil {
				t.Fatal("a trip must be audited")
			}
			if p["reason"] != tc.wantReason || p["threshold_name"] != tc.wantReason {
				t.Errorf("audit names the wrong breaker: reason=%v threshold_name=%v", p["reason"], p["threshold_name"])
			}
			if p["threshold"] != tc.wantThreshold {
				t.Errorf("threshold = %v, want %d — reporting the repeat threshold for every breaker makes the event self-contradictory", p["threshold"], tc.wantThreshold)
			}
			if p["rejection_class"] != "breaker" {
				t.Errorf("rejection_class = %v, want breaker", p["rejection_class"])
			}
			// Audit honesty: every counter that could have tripped is reported alongside the one
			// that did, so a post-mortem reads the loop's state off the event.
			for _, k := range []string{"streak", "recurrences", "no_progress_streak", "error_magnitude", "best_error_magnitude", "signature"} {
				if _, ok := p[k]; !ok {
					t.Errorf("evidence key %q missing; the stop reason cannot be checked without re-deriving the loop state", k)
				}
			}
			// The counter counts ATTEMPTS and a tripped breaker is not one, so the trip must leave
			// it alone. It used to be bumped to the budget, which was how the breaker stopped the
			// outer loop before anything read tripped — and it made every later reader see
			// "ran out of attempts" where the truth was "gave up after three identical rounds".
			if counter != 0 {
				t.Errorf("a trip must not touch the attempt counter, got counter=%d", counter)
			}
			if !state.tripped {
				t.Error("a trip must set loopState.tripped; it is the stop signal the outer loop reads")
			}
			if state.trippedReason != tc.wantReason {
				t.Errorf("trippedReason = %q, want %q", state.trippedReason, tc.wantReason)
			}
			if fixerCanAttempt(&stubFixer{}, state, counter, 9) {
				t.Error("fixerCanAttempt must be false once the breaker has tripped, whatever the counter says")
			}
		})
	}
}

// The sticky no-op path must report the SAME cause as the round that tripped, not a generic label.
func TestCheckFixLoopBreakers_stickyReasonSurvives(t *testing.T) {
	state := &FixLoopState{tripped: true, trippedReason: FixSkipLoopOscillation}
	counter := 0
	stop, reason := checkFixLoopBreakers(context.Background(), EvalOptions{}, StepCompile,
		"boom", "sig", nil, false, nil, &counter, 4, state)
	if !stop || reason != FixSkipLoopOscillation {
		t.Fatalf("stop=%v reason=%q; a tripped loop must keep reporting the breaker that fired", stop, reason)
	}
}

// Thresholds are configurable, and a non-positive value means "unset", never "disabled".
func TestEvalOptions_breakerThresholds(t *testing.T) {
	def := EvalOptions{}
	if def.repeatStopThreshold() != FixLoopRepeatStopThreshold ||
		def.recurrenceStopThreshold() != FixLoopRecurrenceStopThreshold ||
		def.noProgressStopThreshold() != FixLoopNoProgressStopThreshold {
		t.Fatal("an unset config must fall back to the package defaults")
	}
	set := EvalOptions{FixLoopRepeatStopThreshold: 11, FixLoopRecurrenceStopThreshold: 12, FixLoopNoProgressStopThreshold: 13}
	if set.repeatStopThreshold() != 11 || set.recurrenceStopThreshold() != 12 || set.noProgressStopThreshold() != 13 {
		t.Fatal("configured thresholds must win")
	}
	off := EvalOptions{FixLoopRepeatStopThreshold: -1}
	if off.repeatStopThreshold() != FixLoopRepeatStopThreshold {
		t.Fatal("a negative value means unset, not disabled — a disabled breaker burns the whole budget")
	}
}

// A round whose primary diagnostic is a PARSE failure must not move the magnitude baseline or the
// no-progress streak. A file that does not parse aborts the compiler before attribution, so the
// output shrinks and the magnitude measures the masking, not progress — which made healthy rounds
// afterwards read as "no progress" and tripped the breaker on a converging loop.
func TestCheckFixLoopBreakers_parseFailureDoesNotMoveProgress(t *testing.T) {
	state := &FixLoopState{magnitudeKnown: true, bestMagnitude: 3, noProgressStreak: 1}
	counter := 0
	parseOut := "src/test/java/p/ATest.java:[12,5] class, interface, enum, or record expected\n"
	if !ParsePrimaryFailureSite(parseOut).ParseFailure {
		t.Skip("fixture is not recognised as a parse failure")
	}
	checkFixLoopBreakers(context.Background(), EvalOptions{}, StepCompile, parseOut, "sig-parse", nil, false, nil, &counter, 9, state)

	if state.bestMagnitude != 3 {
		t.Errorf("bestMagnitude moved to %d on a parse-broken round; the baseline now measures masking", state.bestMagnitude)
	}
	if state.noProgressStreak != 1 {
		t.Errorf("noProgressStreak moved to %d on a parse-broken round", state.noProgressStreak)
	}
}

// Item 2: the context budget sheds read-only dependencies, never the writable artifacts — a fix
// round without the file it must rewrite is useless, and a truncated one yields a corrupt rewrite.
func TestClampFixContextRunes_protectsWritableArtifacts(t *testing.T) {
	files := map[string]string{
		"a/ATest.java": strings.Repeat("w", 400),
		"src/Big.java": strings.Repeat("x", 900),
		"src/Mid.java": strings.Repeat("y", 500),
	}
	dropped := clampFixContextRunes(files, []string{"a/ATest.java"}, 1000)

	if _, ok := files["a/ATest.java"]; !ok {
		t.Fatal("the writable artifact was shed; the fixer cannot rewrite a file it was not given")
	}
	if len(dropped) == 0 {
		t.Fatal("an over-budget context must shed something")
	}
	// Largest-first reaches the budget in the fewest drops.
	if dropped[0] != "src/Big.java" && len(dropped) == 1 {
		t.Errorf("dropped = %v, want the largest dependency first", dropped)
	}
	if clampFixContextRunes(map[string]string{"a": "small"}, nil, 0) != nil {
		t.Error("an uncapped budget must shed nothing")
	}
}

// Item 2: the backoff is BETWEEN attempts — charging it up front would add latency to every
// single-round repair — and a cancelled run must not be held hostage by it.
func TestSleepBetweenFixAttempts(t *testing.T) {
	if err := sleepBetweenFixAttempts(context.Background(), time.Hour, 1, nil); err != nil {
		t.Fatalf("attempt 1 must never sleep: %v", err)
	}
	if err := sleepBetweenFixAttempts(context.Background(), 0, 5, nil); err != nil {
		t.Fatalf("a zero backoff must not sleep: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepBetweenFixAttempts(ctx, time.Hour, 2, nil); err == nil {
		t.Fatal("a cancelled run must not wait out the backoff")
	}
}

// Item 3: the LLM summary is attached BESIDE the compiler text, never in place of it. Replacing it
// makes a post-mortem impossible without re-running — and worse when the prose is wrong.
func TestMergeFixRequestAuditErrorOutput_summaryNeverReplacesTheLog(t *testing.T) {
	raw := "[ERROR] /workspace/a/ATest.java:[7,3] cannot find symbol: method saveOrder(Order)\n"
	p := mergeFixRequestAuditErrorOutput(raw, raw, false, false, "probably missing Spring Boot test dependencies")

	got, _ := p["error_output"].(string)
	if !strings.Contains(got, "cannot find symbol") {
		t.Fatalf("the compiler text must survive: %q", got)
	}
	if p["error_output_llm_summary"] != "probably missing Spring Boot test dependencies" {
		t.Errorf("the summary must be attached beside it: %v", p["error_output_llm_summary"])
	}
	if p["error_output_llm_summary_used"] != true {
		t.Error("a reader must be able to tell a summary was produced")
	}

	none := mergeFixRequestAuditErrorOutput(raw, raw, false, false, "")
	if _, ok := none["error_output_llm_summary"]; ok {
		t.Error("no summary means no key, so absence and empty are not confusable")
	}
}

// Item 3: the feature is off when disabled, unwired, or the log is small enough to read as-is.
func TestComputeErrorLogLLMSummary_gates(t *testing.T) {
	called := 0
	summarizer := func(context.Context, string) (string, error) {
		called++
		return "  a summary  ", nil
	}
	big := strings.Repeat("e", errorLogLLMSummaryMinRunes+1)

	if got := computeErrorLogLLMSummary(context.Background(), EvalOptions{ErrorLogSummarizer: summarizer}, big); got != "a summary" {
		t.Errorf("summary = %q, want the trimmed text", got)
	}
	if got := computeErrorLogLLMSummary(context.Background(),
		EvalOptions{ErrorLogSummarizer: summarizer, DisableErrorLogLLMSummary: true}, big); got != "" {
		t.Errorf("disabled must produce nothing, got %q", got)
	}
	if got := computeErrorLogLLMSummary(context.Background(), EvalOptions{}, big); got != "" {
		t.Errorf("no summarizer must produce nothing, got %q", got)
	}
	if got := computeErrorLogLLMSummary(context.Background(), EvalOptions{ErrorLogSummarizer: summarizer}, "small"); got != "" {
		t.Errorf("a small log needs no summary, got %q", got)
	}
	if called != 1 {
		t.Errorf("the summarizer ran %d time(s); the gates must short-circuit before the LLM call", called)
	}
}

// Item 4: a compile-broken iteration PAUSES the repeated-test-failure streak instead of resetting
// it.
//
// The iteration's test step never runs, so there is no fingerprint to compare. Resetting made the
// early-exit discard unreachable whenever a fixer's own test-step writes broke compilation between
// two identical test failures — the file shielded itself from discard by breaking the build. This
// scans the compile-failure branch for the reset, because the behaviour is a few lines inside a
// long loop and a future edit could reinstate it without any test noticing.
func TestRunEvaluation_compileFailureDoesNotResetTheFailureStreak(t *testing.T) {
	src := readWorkflowSource(t)
	i := strings.Index(src, "if !compileRes.OK {")
	if i < 0 {
		t.Fatal("compile-failure branch not found; this guard needs repointing")
	}
	// The branch runs until the audit call that follows it; the reset, if reinstated, lands here.
	branch := src[i:]
	if j := strings.Index(branch, "out.LastFixAction = FixImportsMocks"); j > 0 {
		branch = branch[:j]
	}
	if strings.Contains(branch, "unitFailStreak, e2eFailStreak = 0, 0") {
		t.Error("the compile-failure branch resets the repeated-failure streaks; a fixer write that " +
			"breaks compilation between two identical test failures then shields its file from discard")
	}
	if !strings.Contains(branch, "PAUSES") {
		t.Error("the pause must be stated where the reset used to be, or it reads as an omission")
	}
}

// A fingerprint that genuinely changes still resets the streak, so the pause cannot mask real
// movement.
func TestMaybeExitOnRepeatedTestFailure_differentFingerprintResets(t *testing.T) {
	opts := EvalOptions{ArtifactPaths: []string{"a/ATest.java", "a/BTest.java"}}
	out := &EvalWorkflowResult{}
	streak, fp := 2, "stale-fingerprint"

	maybeExitOnRepeatedTestFailure(context.Background(), opts, StepTest,
		"FAILED a/ATest.java\n", out, nil, &streak, &fp, 5, 0, 0, true)

	if streak != 1 {
		t.Fatalf("streak = %d; a changed fingerprint must restart the count at 1", streak)
	}
}
