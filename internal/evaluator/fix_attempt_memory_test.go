package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func javaFixRepo(t *testing.T, body string) (repo, rel string) {
	t.Helper()
	repo = t.TempDir()
	rel = "src/test/java/p/OwnerTest.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, rel
}

// The acceptance criterion: after a round, the NEXT round's request must carry what was tried.
// llmfix's raw conversation cannot do this — a real fix prompt is 141-147k runes against a 64k
// retention budget, so history is wiped every round — which is why rounds 3 and 4 of the motivating
// run produced byte-identical compiler output.
func TestApplyLLMFix_secondRoundCarriesPriorAttempt(t *testing.T) {
	before := "package p;\n\nclass OwnerTest {\n\t@Test void a() { assertThat(() -> x()).isNull(); }\n}\n"
	after := "package p;\n\nclass OwnerTest {\n\t@Test void a() { assertThat(y()).isNull(); }\n}\n"
	repo, rel := javaFixRepo(t, before)

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: after}}}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}
	state := &FixLoopState{}
	errOut := "[ERROR] /workspace/" + rel + ":[4,20] reference to assertThat is ambiguous\n"

	// Round 1 — no memory yet.
	applyLLMFix(context.Background(), opts, StepCompile, errOut, &recordingAuditor{}, new(int), 3, state, "")
	if len(fixer.req.PriorAttempts) != 0 {
		t.Fatalf("first round must carry no memory, got %+v", fixer.req.PriorAttempts)
	}

	// Round 2 — the same failure comes back, as it did in production.
	fixer.resp = FixResponse{Files: map[string]string{rel: after}}
	applyLLMFix(context.Background(), opts, StepCompile, errOut, &recordingAuditor{}, new(int), 3, state, "")

	if len(fixer.req.PriorAttempts) != 1 {
		t.Fatalf("second round carries %d prior attempt(s), want 1 — the fixer is answering statelessly", len(fixer.req.PriorAttempts))
	}
	rec := fixer.req.PriorAttempts[0]
	if rec.FailureSignature == "" {
		t.Error("record must carry the failure signature so a repeat is recognisable")
	}
	change, ok := rec.Changes[rel]
	if !ok {
		t.Fatalf("record has no change for %s: %+v", rel, rec.Changes)
	}
	// The diff must describe what actually landed.
	if !strings.Contains(change, "assertThat(y())") {
		t.Errorf("change excerpt does not describe the applied edit:\n%s", change)
	}
}

// The record must describe what reached DISK, not what the model returned. A record claiming an
// edit that a gate rejected would teach the model that an approach failed when it was never tried.
func TestFixAttemptMemory_recordsOnlyWhatWasApplied(t *testing.T) {
	// Two tests before; the model returns a version with one deleted, which the coverage gate blocks.
	before := "package p;\n\nclass OwnerTest {\n\t@Test void a() {}\n\t@Test void b() {}\n}\n"
	deleted := "package p;\n\nclass OwnerTest {\n\t@Test void a() {}\n}\n"
	repo, rel := javaFixRepo(t, before)

	fixer := &stubFixer{resp: FixResponse{Files: map[string]string{rel: deleted}}}
	opts := EvalOptions{RepoPath: repo, Lang: "java", Fixer: fixer, ArtifactPaths: []string{rel}}
	state := &FixLoopState{}
	errOut := "[ERROR] /workspace/" + rel + ":[4,9] cannot find symbol\n"

	applyLLMFix(context.Background(), opts, StepCompile, errOut, &recordingAuditor{}, new(int), 3, state, "")
	applyLLMFix(context.Background(), opts, StepCompile, errOut, &recordingAuditor{}, new(int), 3, state, "")

	if len(fixer.req.PriorAttempts) != 1 {
		t.Fatalf("want 1 prior attempt, got %d", len(fixer.req.PriorAttempts))
	}
	rec := fixer.req.PriorAttempts[0]
	if _, applied := rec.Changes[rel]; applied {
		t.Errorf("record claims an edit that the coverage gate rejected: %+v", rec.Changes)
	}
	if reason := rec.Skipped[rel]; !strings.Contains(reason, "delete tests") {
		t.Errorf("record must say why the write was skipped; got %q", reason)
	}
}

func TestRenderFixAttemptMemory(t *testing.T) {
	if got := RenderFixAttemptMemory(nil); got != "" {
		t.Errorf("no records must render nothing, got %q", got)
	}
	block := RenderFixAttemptMemory([]FixAttemptRecord{
		{Iteration: 0, FailureSignature: "abcdef1234567890", Changes: map[string]string{"A.java": "- old\n+ new"}},
		{Iteration: 1, FailureSignature: "abcdef1234567890", Skipped: map[string]string{"B.java": "would delete tests"}},
	})
	for _, want := range []string{"PRIOR ATTEMPTS", "attempt 1", "attempt 2", "A.java", "+ new", "B.java", "NOT applied"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "abcdef1234567890") {
		t.Error("full signature should be shortened for readability")
	}
}

// The block is bounded, and the OLDEST rounds go first — the most recent attempt is the one the
// model is about to repeat.
func TestRenderFixAttemptMemory_boundedDroppingOldestFirst(t *testing.T) {
	big := strings.Repeat("x", 900)
	var records []FixAttemptRecord
	for i := 0; i < 12; i++ {
		records = append(records, FixAttemptRecord{
			Iteration:        i,
			FailureSignature: "sig",
			Changes:          map[string]string{"F.java": big + "\nmarker" + string(rune('A'+i))},
		})
	}
	block := RenderFixAttemptMemory(records)
	if runeLen(block) > maxAttemptMemoryRunes+maxAttemptRecordRunes {
		t.Errorf("block is %d runes, well past the budget", runeLen(block))
	}
	if !strings.Contains(block, "markerL") {
		t.Error("the most recent attempt must survive trimming")
	}
	if strings.Contains(block, "markerA") {
		t.Error("the oldest attempt should have been dropped first")
	}
}

func TestRecordFixAttempt_boundsHistory(t *testing.T) {
	state := &FixLoopState{}
	for i := 0; i < 20; i++ {
		recordFixAttempt(state, i, "sig", map[string]string{"A.java": "+ x"}, nil)
	}
	if len(state.attempts) > 8 {
		t.Errorf("history grew unbounded: %d records", len(state.attempts))
	}
	if state.attempts[len(state.attempts)-1].Iteration != 19 {
		t.Error("newest record must be retained")
	}
	// A round that neither applied nor skipped anything is not worth remembering.
	before := len(state.attempts)
	recordFixAttempt(state, 20, "sig", nil, nil)
	if len(state.attempts) != before {
		t.Error("an empty round should not be recorded")
	}
	recordFixAttempt(nil, 0, "sig", map[string]string{"A": "x"}, nil) // must not panic
}

func TestSummarizeAppliedChange(t *testing.T) {
	if got := summarizeAppliedChange("", "anything"); got != "(new file)" {
		t.Errorf("new file = %q", got)
	}
	got := summarizeAppliedChange("a\nb\nc\n", "a\nB\nc\n")
	if !strings.Contains(got, "- b") || !strings.Contains(got, "+ B") {
		t.Errorf("change excerpt missing the edit: %q", got)
	}
	if got := summarizeAppliedChange("a\n", "a\n"); !strings.Contains(got, "no net line change") {
		t.Errorf("identical content = %q", got)
	}
}
