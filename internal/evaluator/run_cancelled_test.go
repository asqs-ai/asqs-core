package evaluator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// countingSandboxRunner reports how many times the compile step actually ran, which is what a
// cancelled loop must stop spending.
type countingSandboxRunner struct {
	stubSandboxRunner
	compiles int
	// cancel is invoked on the Nth compile, standing in for an operator interrupt or a run deadline
	// landing mid-step.
	cancelOn int
	cancel   context.CancelFunc
}

func (c *countingSandboxRunner) Compile(ctx context.Context, repoPath, lang string) StepResult {
	c.compiles++
	if c.compiles == c.cancelOn && c.cancel != nil {
		c.cancel()
		return StepResult{
			Step: StepCompile, OK: false,
			Summary: "compile step was cancelled before it completed",
			Output:  "compile step was cancelled before it completed",
		}
	}
	return c.stubSandboxRunner.compile
}

// A cancelled context ends the loop instead of spending the rest of the iteration budget on steps
// that return instantly. Before this, audit.log of 2026-08-29 recorded iterations 20-40 all firing
// within one millisecond after the cancellation at 08:39:31.
func TestRunEvaluation_cancelledContextStopsTheLoop(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/OwnerTest.java"
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("class T {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &countingSandboxRunner{
		stubSandboxRunner: stubSandboxRunner{
			compile: StepResult{Step: StepCompile, OK: false, Output: stuckCompileError, Summary: "compile failed"},
		},
		cancelOn: 3,
		cancel:   cancel,
	}
	opts := DefaultEvalOptions(dir, "java")
	opts.MaxFixIterations = 40
	opts.ArtifactPaths = []string{rel}
	audit := &recordingAuditor{}

	result, err := RunEvaluation(ctx, runner, opts, audit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; a cancelled run must surface context.Canceled, not a nil/ordinary result", err)
	}
	if runner.compiles != 3 {
		t.Errorf("compile ran %d times; want 3 — the loop must stop at the top of the next iteration, not keep re-running the step", runner.compiles)
	}
	if result.Iterations != 3 {
		t.Errorf("Iterations = %d; want 3 (the last iteration that actually ran), not the full budget of %d", result.Iterations, opts.MaxFixIterations)
	}
	if !audit.hasStep("evaluator.run_cancelled") {
		t.Error("expected an evaluator.run_cancelled audit event naming the cancellation")
	}
}
