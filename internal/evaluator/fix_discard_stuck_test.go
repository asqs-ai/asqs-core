package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const stuckCompileError = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/petclinic/PetValidatorTest.java:[14,51] cannot find symbol
  symbol:   class PetType
  location: package petclinic.model
[ERROR] BUILD FAILURE`

// exitByDiscardingStuckArtifacts must attribute a compile-shaped failure to the offending generated
// artifact, keep the others, and set the EarlyExit* contract.
func TestExitByDiscardingStuckArtifacts_compileShapedSubset(t *testing.T) {
	opts := DefaultEvalOptions("/repo", "java")
	opts.ArtifactPaths = []string{"src/test/java/petclinic/PetValidatorTest.java", "src/test/java/petclinic/OwnerTest.java"}
	var out EvalWorkflowResult
	audit := &recordingAuditor{}
	ok := exitByDiscardingStuckArtifacts(context.Background(), opts, StepTest, stuckCompileError, &out, audit, 0, 3, true)
	if !ok {
		t.Fatal("want exit=true when a generated artifact is the cited compile failure")
	}
	if len(out.EarlyExitDiscardPaths) != 1 || filepath.Base(out.EarlyExitDiscardPaths[0]) != "PetValidatorTest.java" {
		t.Fatalf("EarlyExitDiscardPaths = %v; want only PetValidatorTest.java", out.EarlyExitDiscardPaths)
	}
	if !out.EarlyExitStableAfterDiscard || !out.Stable {
		t.Errorf("a passing sibling artifact remains => stable-after-discard; got stable=%v afterDiscard=%v", out.Stable, out.EarlyExitStableAfterDiscard)
	}
	if !audit.hasStep("evaluator.fix_loop_stuck_artifact_discarded") {
		t.Error("expected evaluator.fix_loop_stuck_artifact_discarded audit event")
	}
}

// Threshold of -1 disables discard (operator opt-out): the loop should run to budget instead.
func TestExitByDiscardingStuckArtifacts_disabledByThreshold(t *testing.T) {
	opts := DefaultEvalOptions("/repo", "java")
	opts.ArtifactPaths = []string{"src/test/java/petclinic/PetValidatorTest.java"}
	opts.RepeatedTestFailureThreshold = -1
	var out EvalWorkflowResult
	if exitByDiscardingStuckArtifacts(context.Background(), opts, StepTest, stuckCompileError, &out, nil, 0, 3, true) {
		t.Fatal("discard must be disabled when RepeatedTestFailureThreshold < 0")
	}
}

// A compile error in a non-generated file must not cause any discard (we only ever drop files we wrote).
func TestExitByDiscardingStuckArtifacts_noDiscardWhenFailureNotInArtifacts(t *testing.T) {
	opts := DefaultEvalOptions("/repo", "java")
	opts.ArtifactPaths = []string{"src/test/java/petclinic/OwnerTest.java"}
	var out EvalWorkflowResult
	if exitByDiscardingStuckArtifacts(context.Background(), opts, StepTest, stuckCompileError, &out, nil, 0, 3, true) {
		t.Fatal("must not discard when the cited failure is not a generated artifact")
	}
}

// movingFixer always "fixes" the same file with a fresh body, so each fix is applied but the runner keeps
// reporting the same compile error — the stuck loop the breaker+discard must terminate.
type movingFixer struct {
	path string
	n    int
}

func (m *movingFixer) Fix(ctx context.Context, req FixRequest) (FixResponse, error) {
	m.n++
	body := fmt.Sprintf("package petclinic;\nimport org.junit.jupiter.api.Test;\nclass PetValidatorTest { @Test void t%d() { new petclinic.PetValidator().validate(); } }\n", m.n)
	return FixResponse{Files: map[string]string{m.path: body}}, nil
}

var _ Fixer = (*movingFixer)(nil)

// End-to-end RC2+RC3: a test suite whose compile keeps failing on one artifact must terminate the loop
// early (breaker trips, stuck artifact discarded) rather than burning all 30 iterations, and must ship
// the remaining tests (stable-after-discard).
func TestRunEvaluation_stuckCompileError_discardsOneArtifactAndStaysStable(t *testing.T) {
	dir := t.TempDir()
	keep := "src/test/java/petclinic/PetValidatorTest.java"
	other := "src/test/java/petclinic/OwnerTest.java"
	for _, rel := range []string{keep, other} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("class T {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true},
		test:     StepResult{Step: StepTest, OK: false, Output: stuckCompileError, Summary: "compile error in test sources"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 30
	opts.CompileOncePerEval = true
	opts.ArtifactPaths = []string{keep, other}
	opts.Fixer = &movingFixer{path: keep}
	audit := &recordingAuditor{}

	result, err := RunEvaluation(context.Background(), runner, opts, audit)
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if result.Iterations >= opts.MaxFixIterations {
		t.Errorf("loop ran to the full budget (%d iterations); RC2/RC3 should have terminated it early", result.Iterations)
	}
	if len(result.EarlyExitDiscardPaths) != 1 || filepath.Base(result.EarlyExitDiscardPaths[0]) != "PetValidatorTest.java" {
		t.Fatalf("EarlyExitDiscardPaths = %v; want only the offending PetValidatorTest.java", result.EarlyExitDiscardPaths)
	}
	if !result.EarlyExitStableAfterDiscard || !result.Stable {
		t.Errorf("the other generated test should survive => stable-after-discard; stable=%v afterDiscard=%v", result.Stable, result.EarlyExitStableAfterDiscard)
	}
	if !audit.hasStep("evaluator.fix_rejected_low_value") {
		t.Error("expected the circuit-breaker to log evaluator.fix_rejected_low_value before discard")
	}
	if !audit.hasStep("evaluator.fix_loop_stuck_artifact_discarded") {
		t.Error("expected evaluator.fix_loop_stuck_artifact_discarded")
	}
}
