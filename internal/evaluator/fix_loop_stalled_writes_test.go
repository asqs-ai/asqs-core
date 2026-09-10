package evaluator

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stalledWriteRound drives one applyLLMFix round in which the model rewrites the BLAMED line of
// the artifact and the artifact's diagnostic comes back identical anyway.
//
// The fixture has to dodge three existing breakers to isolate the fourth, and each dodge
// corresponds to a real property of the run this breaker was written for:
//
//   - it edits the blamed line, so fix_primary_site_never_touched does not fire (the observed
//     fixer WAS editing the right file — that is what made the stall invisible);
//   - a second file's diagnostic moves every round, so the (step, paths, error_output) signature
//     never repeats and neither fix_loop_repeat nor fix_loop_oscillation can match — the same
//     effect line-number drift had in the real run;
//   - the error-line count is constant, so fix_loop_no_progress advances but, at a threshold of 5,
//     more slowly than this breaker's 3.
type stalledWriteFixture struct {
	repo, rel string
	base      string
	state     *FixLoopState
	opts      EvalOptions
	audit     *recordingAuditor
}

func newStalledWriteFixture(t *testing.T) *stalledWriteFixture {
	t.Helper()
	f := &stalledWriteFixture{
		repo:  t.TempDir(),
		rel:   "src/test/java/p/VetsTests.java",
		base:  "package p;\n\nclass VetsTests {\n\t@Test void a() {\n\t\tvets.setVets(%s);\n\t}\n}\n",
		state: &FixLoopState{},
		audit: &recordingAuditor{},
	}
	writeRepoFile(t, f.repo, f.rel, fmt.Sprintf(f.base, "list"))
	f.opts = EvalOptions{RepoPath: f.repo, Lang: "java", ArtifactPaths: []string{f.rel}}
	return f
}

// stalledOutput keeps the artifact's own diagnostic byte-identical while another file's moves.
func (f *stalledWriteFixture) stalledOutput(round int) string {
	return "[ERROR] /workspace/" + f.rel + ":[5,21] cannot find symbol\n  symbol:   method setVets(java.util.List<p.Vet>)\n" +
		fmt.Sprintf("[ERROR] /workspace/src/test/java/p/OtherTests.java:[%d,9] cannot find symbol\n", 40+round)
}

// run rewrites the blamed line with a fresh (still wrong) argument, so the write always lands.
func (f *stalledWriteFixture) run(round int, errOut string) {
	body := fmt.Sprintf(f.base, fmt.Sprintf("list%d", round))
	f.opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{f.rel: body}}}
	applyLLMFix(context.Background(), f.opts, StepCompile, errOut, f.audit, new(int), 40, f.state, "")
}

// The evaluator already has the decisive evidence and throws it away: stalledFiles compares
// per-file diagnostics across rounds and raises evaluator.fix_file_no_progress, which reached the
// model's prompt and the audit and nothing else. An asqs-go React run burned four such rounds —
// 16:56 to 17:50 — writing one file to no effect before the model stopped returning writes at all.
func TestFixLoop_stopsWhenEveryWriteLeavesTheDiagnosticsUnchanged(t *testing.T) {
	f := newStalledWriteFixture(t)

	rounds := 0
	for i := 0; i < 12 && !f.state.tripped; i++ {
		f.run(i, f.stalledOutput(i))
		rounds++
	}

	if !f.state.tripped {
		t.Fatalf("the loop ran %d rounds of writes that moved nothing and never stopped", rounds)
	}
	if f.state.trippedReason != FixSkipLoopWritesStalled {
		t.Fatalf("tripped reason = %q, want %q (another breaker beat it to the stop)", f.state.trippedReason, FixSkipLoopWritesStalled)
	}
	// Round 1 has no prior round to judge; rounds 2..4 each report a stall, and the breaker is
	// checked at the START of a round, so round 5 is where it fires.
	if rounds != 5 {
		t.Errorf("stopped after %d rounds, want 5", rounds)
	}
	if !f.audit.hasStep("evaluator.fix_file_no_progress") {
		t.Error("the per-file evidence should still reach the model")
	}
	var msg string
	for _, p := range f.audit.payloads["evaluator.fix_rejected_low_value"] {
		if r, _ := p["reason"].(string); r == FixSkipLoopWritesStalled {
			msg, _ = p["message"].(string)
		}
	}
	if !strings.Contains(msg, "byte-identical diagnostics") {
		t.Errorf("the stop should say what it observed; got %q", msg)
	}
}

// A round whose write DOES move the diagnostic clears the streak, so an intermittently-stalling
// fixer is not retired for its bad rounds alone.
func TestFixLoop_stalledWriteStreakResetsOnProgress(t *testing.T) {
	f := newStalledWriteFixture(t)

	// Real progress: the artifact's diagnostic changes AND the build gets smaller, which also
	// resets the magnitude breaker so it cannot be what keeps the loop alive here.
	progress := "[ERROR] /workspace/" + f.rel + ":[5,21] incompatible types: String cannot be converted to int\n"

	f.run(0, f.stalledOutput(0))
	f.run(1, f.stalledOutput(1))
	f.run(2, f.stalledOutput(2))
	if f.state.stalledWriteStreak != 2 {
		t.Fatalf("stalledWriteStreak = %d after two stalled reports, want 2", f.state.stalledWriteStreak)
	}
	f.run(3, progress)
	if f.state.stalledWriteStreak != 0 {
		t.Fatalf("a round that moved the diagnostic must reset the streak; got %d", f.state.stalledWriteStreak)
	}
	f.run(4, f.stalledOutput(4))
	f.run(5, f.stalledOutput(5))

	if f.state.tripped {
		t.Fatalf("two stalled rounds after a real repair must not stop the loop; tripped with %q", f.state.trippedReason)
	}
}
