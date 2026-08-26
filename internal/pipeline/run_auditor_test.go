package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// fakeSandbox reports every step green, so RunEvaluation runs its full audited path without any
// toolchain present.
type fakeSandbox struct{}

func (fakeSandbox) Compile(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "fake compile"}
}
func (fakeSandbox) Test(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "fake test"}
}
func (fakeSandbox) Lint(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepLint, OK: true, Summary: "fake lint"}
}
func (fakeSandbox) Coverage(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: "fake coverage"}
}
func (fakeSandbox) Mutation(_ context.Context, _, _ string, _ []string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepMutation, OK: true, Summary: "skipped"}
}

type auditLine struct {
	TS      string                 `json:"ts"`
	Step    string                 `json:"step"`
	Level   string                 `json:"level"`
	Payload map[string]interface{} `json:"payload"`
}

func readAuditLines(t *testing.T, path string) []auditLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []auditLine
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var l auditLine
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("audit line %q is not valid JSON: %v", ln, err)
		}
		out = append(out, l)
	}
	return out
}

// The integration half of the audit-sink acceptance: a real evaluation pass through the pipeline's
// auditor produces JSONL where the steps carry their structured payloads — the payloads that were
// previously discarded.
func TestBuildRunAuditor_evaluationPayloadsLandInJSONL(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	auditor, closeAudit := buildRunAuditor(logPath, false)

	res, err := evaluator.RunEvaluation(context.Background(), fakeSandbox{}, evaluator.EvalOptions{
		RepoPath:           t.TempDir(),
		Lang:               "java",
		MaxFixIterations:   2,
		ArtifactPaths:      []string{"src/test/java/FooTest.java"},
		CompileOncePerEval: true,
	}, auditor)
	closeAudit()
	if err != nil {
		t.Fatalf("RunEvaluation: %v", err)
	}
	if !res.Stable {
		t.Fatalf("all-green fake sandbox should yield a stable result, got %+v", res)
	}

	lines := readAuditLines(t, logPath)
	if len(lines) == 0 {
		t.Fatal("audit file is empty")
	}
	var sawIteration, sawStep bool
	for _, l := range lines {
		switch l.Step {
		case "evaluator.iteration":
			sawIteration = true
			if l.Payload["iteration"] != float64(1) || l.Payload["max"] != float64(2) {
				t.Errorf("evaluator.iteration payload = %v, want iteration=1 max=2", l.Payload)
			}
		case "evaluator.step":
			sawStep = true
			if l.Payload["ok"] != true {
				t.Errorf("evaluator.step payload = %v, want ok=true", l.Payload)
			}
			if s, _ := l.Payload["step"].(string); s == "" {
				t.Errorf("evaluator.step payload has no step name: %v", l.Payload)
			}
		}
	}
	if !sawIteration || !sawStep {
		t.Errorf("audit file lacks expected steps (iteration=%v step=%v); lines: %+v", sawIteration, sawStep, lines)
	}
}

func TestBuildRunAuditor_emptyPathKeepsTodaysBehaviour(t *testing.T) {
	a, closeFn := buildRunAuditor("", false)
	defer closeFn()
	if _, ok := a.(stderrAuditor); !ok {
		t.Fatalf("empty path built %T, want the plain stderr auditor", a)
	}
}

// An unopenable audit path degrades to the stderr auditor instead of failing the run — the
// optional file sink must never take the run down, nor disable the stderr lines with it.
func TestBuildRunAuditor_unopenablePathDegradesToStderr(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, closeFn := buildRunAuditor(filepath.Join(blocker, "audit.jsonl"), false)
	defer closeFn()
	if _, ok := a.(stderrAuditor); !ok {
		t.Fatalf("unopenable path built %T, want degradation to the stderr auditor", a)
	}
}
