package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type planRecordingAuditor struct {
	steps    []string
	payloads []map[string]interface{}
}

func (a *planRecordingAuditor) Log(_ context.Context, step string, payload interface{}) {
	a.steps = append(a.steps, step)
	if m, ok := payload.(map[string]interface{}); ok {
		a.payloads = append(a.payloads, m)
	}
}
func (a *planRecordingAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	a.Log(ctx, step, payload)
}

func writePlanFixtureFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (a *planRecordingAuditor) payloadFor(step string) map[string]interface{} {
	for i, s := range a.steps {
		if s == step && i < len(a.payloads) {
			return a.payloads[i]
		}
	}
	return nil
}

// The evaluation's effective argv is written to the runlog and nowhere else, and for an API run
// the runlog is the server process's own stderr — not persisted, gone by the time anyone reads the
// audit.
//
// That is not a cosmetic gap. In run an asqs-go React run the compile step
// reported `compile ok` on six consecutive rounds while `npm run build` on that exact commit —
// reproduced with the bootstrap's own tsconfig patches applied — fails with
// `TS2304: Cannot find name 'src'` in one second. audit.log cannot say which command produced that
// verdict, whether a config override replaced it, or whether the step was skipped, so the question
// is unanswerable after the fact. The one artifact a post-mortem actually has must carry it.
func TestLogEvalEnvOnce_auditsTheResolvedPlan(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	dir := javaRepoFixture(t)
	audit := &planRecordingAuditor{}
	sb := &Sandbox{Type: "local", Timeout: "30m", BuildTool: "mvn", Audit: audit}
	plan, err := sb.buildStepPlan(dir, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	captureRunnerStderr(t, func() { sb.logEvalEnvOnce(context.Background(), plan, dir) })

	p := audit.payloadFor("runner.eval_plan_resolved")
	if p == nil {
		t.Fatalf("no runner.eval_plan_resolved audit event; got steps %v", audit.steps)
	}
	if got, _ := p["target"].(string); got != "local" {
		t.Errorf("target = %v, want local", p["target"])
	}
	if got, _ := p["lang"].(string); got != "java" {
		t.Errorf("lang = %v, want java", p["lang"])
	}
	if got, _ := p["workdir"].(string); got != dir {
		t.Errorf("workdir = %v, want %s", p["workdir"], dir)
	}
	steps, _ := p["steps"].(map[string]interface{})
	if steps == nil {
		t.Fatalf("payload carries no per-step detail: %v", p)
	}
	compile, _ := steps["compile"].(map[string]interface{})
	if compile == nil {
		t.Fatalf("no compile entry: %v", steps)
	}
	if action, _ := compile["action"].(string); action != "run" {
		t.Errorf("compile action = %v, want run", compile["action"])
	}
	if argv, _ := compile["argv"].(string); !strings.Contains(argv, "test-compile") {
		t.Errorf("compile argv = %q, want the command the executor runs", argv)
	}
	// The message is what a reader sees first; it must name the compile command.
	if msg, _ := p["message"].(string); !strings.Contains(msg, "test-compile") {
		t.Errorf("message = %q, want it to name the compile command", msg)
	}
}

// A step that will NOT run has to say so, and say why. "compile ok" from a skipped step and
// "compile ok" from a passing one are indistinguishable in the audit today.
func TestLogEvalEnvOnce_auditsSkippedStepsWithTheirReason(t *testing.T) {
	dir := t.TempDir()
	// A JS package with no build script: the compile step is a skip, not a pass.
	writePlanFixtureFile(t, dir, "package.json", `{"name":"x","scripts":{"test":"vitest run"}}`)
	audit := &planRecordingAuditor{}
	sb := &Sandbox{Type: "local", Timeout: "30m", Audit: audit}
	plan, err := sb.buildStepPlan(dir, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	captureRunnerStderr(t, func() { sb.logEvalEnvOnce(context.Background(), plan, dir) })

	p := audit.payloadFor("runner.eval_plan_resolved")
	if p == nil {
		t.Fatal("no runner.eval_plan_resolved audit event")
	}
	steps, _ := p["steps"].(map[string]interface{})
	compile, _ := steps["compile"].(map[string]interface{})
	if compile == nil {
		t.Fatalf("no compile entry: %v", steps)
	}
	if action, _ := compile["action"].(string); action != "skip" {
		t.Fatalf("compile action = %v, want skip", compile["action"])
	}
	if reason, _ := compile["reason"].(string); !strings.Contains(reason, "no build script") {
		t.Errorf("compile reason = %q, want it to name the cause", reason)
	}
}

// A configured override is the first thing to check when a step's verdict looks impossible, and it
// is exactly what audit.log could not show.
func TestLogEvalEnvOnce_auditsCommandOverrides(t *testing.T) {
	dir := t.TempDir()
	writePlanFixtureFile(t, dir, "package.json", `{"name":"x","scripts":{"build":"tsc --noEmit","test":"vitest run"}}`)
	audit := &planRecordingAuditor{}
	sb := &Sandbox{Type: "local", Timeout: "30m", Audit: audit, CompileCommand: "npx vite build"}
	plan, err := sb.buildStepPlan(dir, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	captureRunnerStderr(t, func() { sb.logEvalEnvOnce(context.Background(), plan, dir) })

	p := audit.payloadFor("runner.eval_plan_resolved")
	if p == nil {
		t.Fatal("no runner.eval_plan_resolved audit event")
	}
	if got, _ := p["compile_command_override"].(string); got != "npx vite build" {
		t.Errorf("compile_command_override = %v, want the configured value", p["compile_command_override"])
	}
	steps, _ := p["steps"].(map[string]interface{})
	compile, _ := steps["compile"].(map[string]interface{})
	if argv, _ := compile["argv"].(string); !strings.Contains(argv, "vite build") {
		t.Errorf("compile argv = %q, want the override to be visible in what runs", argv)
	}
}

// Nil auditor is the CLI path; it must not panic and must still write the runlog block.
func TestLogEvalEnvOnce_nilAuditorStillLogsToRunlog(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	dir := javaRepoFixture(t)
	sb := &Sandbox{Type: "local", Timeout: "30m", BuildTool: "mvn"}
	plan, err := sb.buildStepPlan(dir, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	out := captureRunnerStderr(t, func() { sb.logEvalEnvOnce(context.Background(), plan, dir) })
	if !strings.Contains(out, "[asqs-eval] evaluation runner: type=local") {
		t.Errorf("runlog block missing:\n%s", out)
	}
}
