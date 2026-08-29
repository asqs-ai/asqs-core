package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeArtifacts materialises each path with a minimal compilable-looking test body.
func writeArtifacts(t *testing.T, dir string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "package petclinic;\nimport org.junit.jupiter.api.Test;\nclass C { @Test void a() {} }\n"
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A compile that never goes green and a fixer that cannot converge must terminate the loop and
// discard the offending artifact — the RC3 exit the test branch has always had and the compile
// branch did not. Before this, the breaker pinned compileFixAttempts to the budget and every
// remaining iteration ran a full container compile with no fixer call: 15 iterations and ~14
// minutes of Docker in the run of 2026-08-29.
func TestRunEvaluation_compileNeverGreen_exitsAndDiscardsCreatedArtifact(t *testing.T) {
	dir := t.TempDir()
	offending := "src/test/java/petclinic/PetValidatorTest.java"
	other := "src/test/java/petclinic/OwnerTest.java"
	writeArtifacts(t, dir, offending, other)

	runner := &stubSandboxRunner{
		compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 30
	opts.ArtifactPaths = []string{offending, other}
	opts.Fixer = &movingFixer{path: offending}
	audit := &recordingAuditor{}

	result, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if result.Iterations >= opts.MaxFixIterations {
		t.Errorf("loop ran to the full budget (%d iterations); the compile branch must stop once the fixer is exhausted", result.Iterations)
	}
	if len(result.EarlyExitDiscardPaths) != 1 || filepath.Base(result.EarlyExitDiscardPaths[0]) != "PetValidatorTest.java" {
		t.Fatalf("EarlyExitDiscardPaths = %v; want only the offending created artifact", result.EarlyExitDiscardPaths)
	}
	if !result.EarlyExitStableAfterDiscard {
		t.Error("the other generated test is untouched by the diagnostic => stable-after-discard")
	}
	if !audit.hasStep("evaluator.fix_loop_stuck_artifact_discarded") {
		t.Error("expected evaluator.fix_loop_stuck_artifact_discarded")
	}
}

// Safety property: an artifact this run APPENDED to an existing repo test is never discarded,
// because the caller implements discard as os.Remove. The loop must still stop early and say why.
func TestRunEvaluation_compileNeverGreen_neverDiscardsExtendedArtifact(t *testing.T) {
	dir := t.TempDir()
	offending := "src/test/java/petclinic/PetValidatorTest.java"
	other := "src/test/java/petclinic/OwnerTest.java"
	writeArtifacts(t, dir, offending, other)

	runner := &stubSandboxRunner{
		compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 30
	opts.ArtifactPaths = []string{offending, other}
	// The blamed file is one the repository already owned; we only appended to it.
	opts.ExtendedArtifactPaths = []string{offending}
	opts.Fixer = &movingFixer{path: offending}
	audit := &recordingAuditor{}

	result, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if len(result.EarlyExitDiscardPaths) != 0 {
		t.Fatalf("EarlyExitDiscardPaths = %v; an extended repo test must never be scheduled for deletion", result.EarlyExitDiscardPaths)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(offending))); err != nil {
		t.Errorf("the extended file must remain on disk: %v", err)
	}
	if result.Iterations >= opts.MaxFixIterations {
		t.Errorf("loop ran to the full budget (%d iterations); it must stop once the fixer is exhausted even when nothing is discardable", result.Iterations)
	}
	if !audit.hasStep("evaluator.discard_withheld_extended_artifact") {
		t.Error("expected evaluator.discard_withheld_extended_artifact naming the file we refused to delete")
	}
	if !audit.hasStep("evaluator.compile_fix_exhausted") {
		t.Error("expected evaluator.compile_fix_exhausted to explain why the loop stopped")
	}
}

// With no fixer at all, nothing between iterations can change a deterministic compile, so the loop
// must stop on the first failure rather than re-running it to the budget.
func TestRunEvaluation_compileFailsWithNoFixer_stopsImmediately(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/PetValidatorTest.java"
	writeArtifacts(t, dir, rel)

	runner := &stubSandboxRunner{
		compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 30
	opts.ArtifactPaths = []string{rel}
	opts.RepeatedTestFailureThreshold = -1 // discard disabled: isolate the exit itself
	audit := &recordingAuditor{}

	result, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d; want 1 — with no fixer the second compile can only repeat the first", result.Iterations)
	}
	if !audit.hasStep("evaluator.compile_fix_exhausted") {
		t.Error("expected evaluator.compile_fix_exhausted")
	}
	p := audit.lastPayload("evaluator.compile_fix_exhausted")
	if p == nil {
		t.Fatal("missing payload")
	}
	if present, _ := p["fixer_present"].(bool); present {
		t.Error("fixer_present must be false when no fixer is configured")
	}
}

// A retryable skip (one unusable model turn) is not terminal: the loop keeps going, because a fresh
// turn may well succeed.
func TestIsTerminalFixSkip_retryableVsTerminal(t *testing.T) {
	terminal := []string{FixSkipLoopRepeat, FixSkipLoopOscillation, FixSkipLoopNoProgress, FixSkipNoWritableArtifacts}
	for _, r := range terminal {
		if !IsTerminalFixSkip(r) {
			t.Errorf("%s must be terminal", r)
		}
	}
	for _, r := range []string{FixSkipResponseUnusable, FixSkipNoAcceptedWrites, ""} {
		if IsTerminalFixSkip(r) {
			t.Errorf("%q must be retryable — a different turn can still succeed", r)
		}
	}
}
