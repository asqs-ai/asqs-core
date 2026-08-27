package evaluator

import (
	"context"
	"strings"
	"testing"
)

// Verbatim from run api-e0982497f502f5daf4aa64b4555c7ffa, which produced this output twice in a
// row while the fixer rewrote VetControllerE2EIT.java and VetsTests.java on both rounds.
const twoFileFailure = `[ERROR] COMPILATION ERROR : 
[ERROR] /workspace/src/test/java/p/VetControllerE2EIT.java:[33,47] cannot find symbol
  symbol:   variable request
  location: variable playwright of type com.microsoft.playwright.Playwright
[ERROR] /workspace/src/test/java/p/VetsTests.java:[53,21] cannot find symbol
  symbol:   method setVets(java.util.List<p.Vet>)
  location: variable vets of type p.Vets
`

func TestFileDiagnostics_groupsDetailLinesWithTheirDiagnostic(t *testing.T) {
	got := FileDiagnostics(twoFileFailure)
	if len(got) != 2 {
		t.Fatalf("got %d file(s), want 2: %v", len(got), got)
	}
	var vet, vets string
	for p, fp := range got {
		switch {
		case strings.HasSuffix(p, "VetControllerE2EIT.java"):
			vet = fp
		case strings.HasSuffix(p, "VetsTests.java"):
			vets = fp
		}
	}
	if vet == "" || vets == "" {
		t.Fatalf("both files must be keyed by path: %v", got)
	}
	if vet == vets {
		t.Error("different diagnostics must not share a fingerprint")
	}
	// The `symbol:` / `location:` detail lines belong to the diagnostic above them: changing one
	// must change that file's fingerprint and nothing else.
	changed := FileDiagnostics(strings.Replace(twoFileFailure, "variable request", "variable requestContext", 1))
	for p, fp := range changed {
		if strings.HasSuffix(p, "VetControllerE2EIT.java") && fp == vet {
			t.Error("a changed detail line must change the owning file's fingerprint")
		}
		if strings.HasSuffix(p, "VetsTests.java") && fp != vets {
			t.Error("the other file's fingerprint must not move")
		}
	}
	if FileDiagnostics("") != nil || FileDiagnostics("no locations here") != nil {
		t.Error("output with no diagnostic locations yields nothing")
	}
}

func TestStalledFiles_onlyCountsAWrittenFileThatDidNotMove(t *testing.T) {
	before := FileDiagnostics(twoFileFailure)
	moved := FileDiagnostics(strings.Replace(twoFileFailure, "[53,21]", "[61,21]", 1))

	written := []string{"src/test/java/p/VetControllerE2EIT.java", "src/test/java/p/VetsTests.java"}
	got := stalledFiles(before, moved, written)
	if len(got) != 1 || !strings.HasSuffix(got[0], "VetControllerE2EIT.java") {
		t.Fatalf("got %v, want only the file whose diagnostics are identical", got)
	}

	// A file that was NOT written is not this signal's business, however static its errors.
	if got := stalledFiles(before, before, []string{"src/test/java/p/VetsTests.java"}); len(got) != 1 {
		t.Errorf("a written file with identical diagnostics must be reported, got %v", got)
	}
	if got := stalledFiles(before, before, nil); got != nil {
		t.Errorf("no writes, nothing to say: %v", got)
	}

	// Errors gone entirely is progress, not a stall.
	gone := FileDiagnostics("[ERROR] /workspace/src/test/java/p/VetsTests.java:[53,21] cannot find symbol\n")
	if got := stalledFiles(before, gone, written); len(got) != 0 {
		t.Errorf("a file whose diagnostics disappeared made progress, got %v", got)
	}
	if got := stalledFiles(nil, before, written); got != nil {
		t.Errorf("no prior failure means no comparison: %v", got)
	}
}

// The signal has to reach the model, not just the audit — that is the whole point of computing it
// before the request is built.
func TestFixAttemptMemory_rendersNoProgressPerFile(t *testing.T) {
	rec := FixAttemptRecord{
		Iteration:        0,
		FailureSignature: "abc123def456",
		Changes: map[string]string{
			"src/test/java/p/VetsTests.java":          "- old\n+ new",
			"src/test/java/p/VetControllerE2EIT.java": "- a\n+ b",
		},
		NoProgress: map[string]bool{"src/test/java/p/VetControllerE2EIT.java": true},
	}
	out := RenderFixAttemptMemory([]FixAttemptRecord{rec})
	if !strings.Contains(out, "VetControllerE2EIT.java: NO EFFECT") {
		t.Errorf("the stalled file must be named as such:\n%s", out)
	}
	if strings.Contains(out, "VetsTests.java: NO EFFECT") {
		t.Errorf("a file that did move must not be labelled:\n%s", out)
	}
}

// End to end through applyLLMFix: round 1 writes, round 2 is told that write achieved nothing.
func TestApplyLLMFix_reportsAFileRewrittenToNoEffect(t *testing.T) {
	repo := t.TempDir()
	const rel = "src/test/java/p/VetsTests.java"
	v1 := "package p;\n\nclass VetsTests {\n\t@Test void a() {\n\t\tvets.setVets(list);\n\t\tint keep = 1;\n\t}\n}\n"
	v2 := strings.Replace(v1, "int keep = 1;", "int keep = 2;", 1)
	writeRepoFile(t, repo, rel, v1)

	// The blamed line is line 5; the model edits line 6 instead. Both rounds "succeed".
	errOut := "[ERROR] /workspace/" + rel + ":[6,21] cannot find symbol\n  symbol:   method setVets(java.util.List<p.Vet>)\n  location: variable vets of type p.Vets\n"

	state := &FixLoopState{}
	audit := &recordingAuditor{}
	opts := EvalOptions{RepoPath: repo, Lang: "java", ArtifactPaths: []string{rel}}

	opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{rel: v2}}}
	if applied, _, _ := applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, new(int), 5, state, ""); !applied {
		t.Fatal("round 1 should have written the file")
	}
	if audit.hasStep("evaluator.fix_file_no_progress") {
		t.Fatal("nothing to compare against on the first round")
	}

	// Same diagnostics come back. Round 2 must be told round 1 achieved nothing.
	opts.Fixer = &stubFixer{resp: FixResponse{Files: map[string]string{rel: strings.Replace(v2, "int keep = 2;", "int keep = 3;", 1)}}}
	applyLLMFix(context.Background(), opts, StepCompile, errOut, audit, new(int), 5, state, "")

	if !audit.hasStep("evaluator.fix_file_no_progress") {
		t.Error("a file rewritten twice with identical diagnostics must be reported")
	}
	if n := len(state.attempts); n == 0 || !state.attempts[0].NoProgress[rel] {
		t.Errorf("the finding must land on the round it judges so the prompt carries it: %+v", state.attempts)
	}
}
