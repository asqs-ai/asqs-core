package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from asqs-go (fix_primary_site_enforcement_test.go).
//
// churningFixer edits the artifact WITHOUT touching the blamed line: it appends a new helper
// method at the end of the file each round — the exact behaviour of run
// api-7549a0ea57f8950449087ff85f1c4ce6, where four rounds wrote VisitControllerTests.java while
// lines 101-104 stayed byte-identical.
type churningFixer struct {
	path  string
	base  string
	calls int
	reqs  []FixRequest
}

func (c *churningFixer) Fix(ctx context.Context, req FixRequest) (FixResponse, error) {
	c.calls++
	c.reqs = append(c.reqs, req)
	var b strings.Builder
	b.WriteString(c.base)
	for i := 1; i <= c.calls; i++ {
		fmt.Fprintf(&b, "\n	void churn%d() {\n		int x%d = %d;\n		if (x%d < 0) {\n			throw new IllegalStateException(\"impossible\");\n		}\n	}\n", i, i, i, i)
	}
	b.WriteString("\n}\n")
	return FixResponse{Files: map[string]string{c.path: b.String()}}, nil
}

const enforcementArtifactBase = `package p;

import org.junit.jupiter.api.Test;

class T {

	@Test
	void broken() {
		notNull(new Object());
	}
`

// The blamed line (the notNull call) never changes; the failure output is constant, so the streak
// identity (path + line-insensitive signature) holds across rounds.
func enforcementCompileFailure(artifact string) StepResult {
	return StepResult{Step: StepCompile, OK: false, Summary: "compile failed", Output: fmt.Sprintf(
		"[ERROR] COMPILATION ERROR :\n[ERROR] /workspace/%s:[9,17] cannot find symbol\n  symbol:   method notNull(java.lang.Object)\n  location: class p.T", artifact)}
}

func TestPrimarySiteEnforcement_forcesFocusThenStops(t *testing.T) {
	dir := t.TempDir()
	artifact := "src/test/java/p/T.java"
	other := "src/test/java/p/OtherTests.java"
	base := enforcementArtifactBase + "\n}\n"
	for _, pair := range [][2]string{
		{artifact, base},
		{other, "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass OtherTests {\n\n\t@Test\n\tvoid fine() {\n\t\tint y = 1;\n\t\tif (y < 0) {\n\t\t\tthrow new IllegalStateException(\"impossible\");\n\t\t}\n\t}\n\n}\n"},
	} {
		full := filepath.Join(dir, filepath.FromSlash(pair[0]))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(pair[1]), 0644); err != nil {
			t.Fatal(err)
		}
	}

	fixer := &churningFixer{path: artifact, base: enforcementArtifactBase}
	audit := &recordingAuditor{}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 10
	opts.Fixer = fixer
	opts.ArtifactPaths = []string{artifact, other}

	var loopState FixLoopState
	counter := 0
	// Drive applyLLMFix directly: each call is one fixer round against the SAME failure. The
	// churning fixer rewrites the file each round (content grows), so every round is an applied
	// write that leaves the blamed line untouched.
	fail := enforcementCompileFailure(artifact)
	var reasons []string
	for round := 1; round <= 5; round++ {
		applied, _, reason := applyLLMFix(context.Background(), opts, StepCompile, fail.Output, audit, &counter, opts.MaxFixIterations, &loopState, "")
		reasons = append(reasons, reason)
		if reason == FixSkipPrimarySiteNeverTouched {
			break
		}
		if !applied {
			t.Fatalf("round %d: expected an applied write (reason=%q)", round, reason)
		}
		// Re-read: the fixer's output landed; disk content grows each round.
		disk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(artifact)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(disk), fmt.Sprintf("churn%d", round)) {
			t.Fatalf("round %d: churn write did not land", round)
		}
	}

	// Rounds 1-2: ordinary, streak builds. Round 3: enforcement (streak==2). Round 4: terminal.
	if len(fixer.reqs) < 3 {
		t.Fatalf("expected at least 3 fixer rounds, got %d (reasons=%v)", len(fixer.reqs), reasons)
	}
	for i, req := range fixer.reqs[:2] {
		if req.PrimarySiteDirective != "" {
			t.Errorf("round %d: no directive expected before the enforcement threshold", i+1)
		}
	}
	forced := fixer.reqs[2]
	if forced.PrimarySiteDirective == "" {
		t.Fatal("round 3: expected the primary-site directive after 2 untouched rounds")
	}
	if !strings.Contains(forced.PrimarySiteDirective, "T.java:9") {
		t.Errorf("directive should name the blamed site; got: %s", forced.PrimarySiteDirective)
	}
	// cannot find symbol => resolution failure => the import repair must be offered.
	if !strings.Contains(forced.PrimarySiteDirective, "import") {
		t.Errorf("resolution-failure directive should offer the import repair; got: %s", forced.PrimarySiteDirective)
	}
	if len(forced.ArtifactPaths) != 1 || forced.ArtifactPaths[0] != artifact {
		t.Errorf("round 3: writable scope should collapse to the blamed file; got %v", forced.ArtifactPaths)
	}
	// Attempt 3 is also the escalation attempt: read-scope narrowing drops the non-writable
	// artifact from Files entirely. On a forced-focus round that is the desired shape — the
	// blamed file is present, the churn outlet is not.
	if _, ok := forced.Files[artifact]; !ok {
		t.Errorf("round 3: the blamed artifact must be in Files; files=%v", enforcementMapKeys(forced.Files))
	}
	if got := reasons[len(reasons)-1]; got != FixSkipPrimarySiteNeverTouched {
		t.Fatalf("expected terminal %q after the forced round failed, got %q (reasons=%v)", FixSkipPrimarySiteNeverTouched, got, reasons)
	}
	if len(fixer.reqs) != 3 {
		t.Errorf("terminal stop must fire BEFORE another LLM call; fixer ran %d rounds", len(fixer.reqs))
	}
	if len(audit.payloads["evaluator.fix_primary_site_enforced"]) != 1 {
		t.Error("expected exactly one fix_primary_site_enforced event")
	}
	if len(audit.payloads["evaluator.fix_primary_site_never_touched"]) != 1 {
		t.Error("expected the terminal fix_primary_site_never_touched event")
	}
	if !IsTerminalFixSkip(FixSkipPrimarySiteNeverTouched) {
		t.Error("fix_primary_site_never_touched must be terminal")
	}
}

// A round that TOUCHES the blamed site must reset the streak: enforcement is for refusal, not for
// slow progress.
func TestPrimarySiteEnforcement_touchResetsStreak(t *testing.T) {
	dir := t.TempDir()
	artifact := "src/test/java/p/T.java"
	base := enforcementArtifactBase + "\n}\n"
	full := filepath.Join(dir, filepath.FromSlash(artifact))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(base), 0644); err != nil {
		t.Fatal(err)
	}

	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 10
	opts.ArtifactPaths = []string{artifact}
	audit := &recordingAuditor{}
	var loopState FixLoopState
	counter := 0
	fail := enforcementCompileFailure(artifact)

	// Round 1: churn (untouched, streak 1).
	churn := &churningFixer{path: artifact, base: enforcementArtifactBase}
	opts.Fixer = churn
	if applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile, fail.Output, audit, &counter, opts.MaxFixIterations, &loopState, ""); !applied {
		t.Fatal("round 1 should apply")
	}
	if loopState.primarySiteUntouchedStreak != 1 {
		t.Fatalf("streak after churn = %d, want 1", loopState.primarySiteUntouchedStreak)
	}

	// Round 2: a fix that adds the static import — for a resolution failure that counts as
	// touching the site (TouchedPrimarySite), so the streak must reset.
	current, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	imported := strings.Replace(string(current),
		"import org.junit.jupiter.api.Test;",
		"import org.junit.jupiter.api.Test;\nimport static org.springframework.util.Assert.notNull;", 1)
	opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{artifact: imported}}}
	if applied, _, reason := applyLLMFix(context.Background(), opts, StepCompile, fail.Output, audit, &counter, opts.MaxFixIterations, &loopState, ""); !applied {
		t.Fatalf("round 2 should apply (reason=%q)", reason)
	}
	if loopState.primarySiteUntouchedStreak != 0 {
		t.Fatalf("streak after import-adding round = %d, want 0 (added import touches a resolution-failure site)", loopState.primarySiteUntouchedStreak)
	}
}

func enforcementMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// otherFileFixer never writes the blamed file: every round it rewrites a sibling artifact. The
// 2026-08-29 run had this shape for 40 iterations (OwnerControllerTests.java:427 blamed, every
// write to VetsTests.java). Such rounds count toward the streak, so the third round is forced onto
// the blamed file alone.
type otherFileFixer struct {
	other string
	calls int
	reqs  []FixRequest
}

func (o *otherFileFixer) Fix(ctx context.Context, req FixRequest) (FixResponse, error) {
	o.calls++
	o.reqs = append(o.reqs, req)
	body := fmt.Sprintf("package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass OtherTests {\n\n\t@Test\n\tvoid fine() {\n\t\tint y = %d;\n\t\tif (y < 0) {\n\t\t\tthrow new IllegalStateException(\"impossible\");\n\t\t}\n\t}\n\n}\n", o.calls)
	return FixResponse{Files: map[string]string{o.other: body}}, nil
}

func TestPrimarySiteEnforcement_neverWritingTheBlamedFileCountsTowardTheStreak(t *testing.T) {
	dir := t.TempDir()
	artifact := "src/test/java/p/T.java"
	other := "src/test/java/p/OtherTests.java"
	for _, pair := range [][2]string{
		{artifact, enforcementArtifactBase + "\n}\n"},
		{other, "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass OtherTests {\n\n\t@Test\n\tvoid fine() {\n\t\tint y = 0;\n\t}\n\n}\n"},
	} {
		full := filepath.Join(dir, filepath.FromSlash(pair[0]))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(pair[1]), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fixer := &otherFileFixer{other: other}
	audit := &recordingAuditor{}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 10
	opts.Fixer = fixer
	opts.ArtifactPaths = []string{artifact, other}
	var loopState FixLoopState
	counter := 0
	fail := enforcementCompileFailure(artifact)
	for round := 1; round <= 3; round++ {
		applied, _, reason := applyLLMFix(context.Background(), opts, StepCompile, fail.Output, audit, &counter, opts.MaxFixIterations, &loopState, "")
		if round < 3 && !applied {
			t.Fatalf("round %d: expected an applied write (reason=%q)", round, reason)
		}
	}
	if len(fixer.reqs) != 3 {
		t.Fatalf("expected 3 fixer rounds, got %d", len(fixer.reqs))
	}
	forced := fixer.reqs[2]
	if forced.PrimarySiteDirective == "" {
		t.Fatal("round 3: expected the primary-site directive after 2 rounds that never wrote the blamed file")
	}
	if len(forced.ArtifactPaths) != 1 || forced.ArtifactPaths[0] != artifact {
		t.Errorf("round 3: writable scope should collapse to the blamed file; got %v", forced.ArtifactPaths)
	}
	if loopState.primarySiteUntouchedStreak < 2 {
		t.Errorf("streak = %d, want >= 2", loopState.primarySiteUntouchedStreak)
	}
}

// Without a forced round the terminal stop must not fire: a blamed file the fixer may not write
// (production source) is bounded by the other breakers, not by this one.
func TestPrimarySiteEnforcement_noTerminalStopWithoutAForcedRound(t *testing.T) {
	dir := t.TempDir()
	other := "src/test/java/p/OtherTests.java"
	full := filepath.Join(dir, filepath.FromSlash(other))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package p;\n\nclass OtherTests {\n\n\tvoid fine() {\n\t\tint y = 0;\n\t}\n\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fixer := &otherFileFixer{other: other}
	audit := &recordingAuditor{}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 10
	opts.Fixer = fixer
	opts.ArtifactPaths = []string{other}
	var loopState FixLoopState
	counter := 0
	// The blamed file is production source, never writable.
	fail := StepResult{Output: "[ERROR] /workspace/src/main/java/p/Owner.java:[12,5] cannot find symbol\n  symbol:   method notNull(java.lang.Object)\n"}
	for round := 1; round <= 4; round++ {
		_, _, reason := applyLLMFix(context.Background(), opts, StepCompile, fail.Output, audit, &counter, opts.MaxFixIterations, &loopState, "")
		if reason == FixSkipPrimarySiteNeverTouched {
			t.Fatalf("round %d: terminal primary-site stop fired although no round could be forced onto the blamed file", round)
		}
	}
	if audit.hasStep("evaluator.fix_primary_site_enforced") || audit.hasStep("evaluator.fix_primary_site_never_touched") {
		t.Error("no enforcement events expected for an unwritable blamed file")
	}
}
