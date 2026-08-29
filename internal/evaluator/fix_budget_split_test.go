package evaluator

import (
	"context"
	"testing"
)

// The per-step repair budget binds independently of the iteration budget. Before this they were one
// number (fixer.iterations.start fed both), so capping model calls also capped the loop.
func TestRunEvaluation_perStepFixBudgetIsIndependentOfIterationBudget(t *testing.T) {
	dir := t.TempDir()
	offending := "src/test/java/petclinic/PetValidatorTest.java"
	other := "src/test/java/petclinic/OwnerTest.java"
	writeArtifacts(t, dir, offending, other)

	runner := &stubSandboxRunner{
		compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 30
	opts.MaxCompileFixAttempts = 2 // far below both the iteration budget and the breaker threshold
	opts.ArtifactPaths = []string{offending, other}
	opts.Fixer = &movingFixer{path: offending}

	result, err := RunEvaluation(context.Background(), runner, opts, &recordingAuditor{})
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if result.CompileFixCount != 2 {
		t.Errorf("CompileFixCount = %d; want exactly the per-step budget of 2, not the iteration budget of %d",
			result.CompileFixCount, opts.MaxFixIterations)
	}
	if result.Iterations >= opts.MaxFixIterations {
		t.Errorf("Iterations = %d; spending the repair budget must not spend the iteration budget", result.Iterations)
	}
}

// Unset per-step budgets fall back to the iteration budget, so existing configurations behave
// exactly as before.
func TestRunEvaluation_unsetPerStepBudgetFallsBackToIterationBudget(t *testing.T) {
	dir := t.TempDir()
	offending := "src/test/java/petclinic/PetValidatorTest.java"
	other := "src/test/java/petclinic/OwnerTest.java"
	writeArtifacts(t, dir, offending, other)

	runner := &stubSandboxRunner{
		compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 2 // and MaxCompileFixAttempts deliberately left at 0
	opts.ArtifactPaths = []string{offending, other}
	opts.Fixer = &movingFixer{path: offending}

	result, err := RunEvaluation(context.Background(), runner, opts, &recordingAuditor{})
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if result.CompileFixCount != 2 {
		t.Errorf("CompileFixCount = %d; an unset per-step budget must inherit MaxFixIterations (2)", result.CompileFixCount)
	}
}

// The audit-honesty payoff: a tripped breaker must leave the attempt count reporting the attempts
// actually made. It used to be bumped to the budget, so a run that gave up after three identical
// rounds reported an exhausted 40-attempt budget instead.
func TestRunEvaluation_trippedBreakerReportsRealAttemptCount(t *testing.T) {
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
	if !audit.hasStep("evaluator.fix_rejected_low_value") {
		t.Fatal("expected the circuit-breaker to fire")
	}
	if result.CompileFixCount >= opts.MaxFixIterations {
		t.Errorf("CompileFixCount = %d of a %d budget; a tripped breaker must not read as an exhausted budget",
			result.CompileFixCount, opts.MaxFixIterations)
	}
	p := audit.lastPayload("evaluator.fix_rejected_low_value")
	if p == nil {
		t.Fatal("missing breaker payload")
	}
	t.Logf("breaker fired at fix_attempt=%v of %v; CompileFixCount=%d", p["fix_attempt"], p["max_fix_attempt"], result.CompileFixCount)
}
