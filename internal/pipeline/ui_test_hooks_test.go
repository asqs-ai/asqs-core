package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
)

const uiTestHooksSampleTSX = `import React from "react";

export function HomePage() {
  return (
    <main>
      <h1>Welcome</h1>
      <button onClick={() => alert("hi")}>Get started</button>
    </main>
  );
}
`

// hooksSandbox answers the compile step with a fixed verdict and records the calls.
type hooksSandbox struct {
	ok     bool
	output string
	calls  int
}

func (s *hooksSandbox) Compile(_ context.Context, _, _ string) evaluator.StepResult {
	s.calls++
	return evaluator.StepResult{Step: evaluator.StepCompile, OK: s.ok, Output: s.output}
}
func (s *hooksSandbox) Test(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{OK: true}
}
func (s *hooksSandbox) Lint(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{OK: true}
}
func (s *hooksSandbox) Coverage(_ context.Context, _, _ string) evaluator.StepResult {
	return evaluator.StepResult{OK: true}
}
func (s *hooksSandbox) Mutation(_ context.Context, _, _ string, _ []string) evaluator.StepResult {
	return evaluator.StepResult{OK: true}
}

// hooksAuditor records every audit event by name.
type hooksAuditor struct {
	mu     sync.Mutex
	events map[string][]map[string]interface{}
}

func (a *hooksAuditor) record(step string, payload interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.events == nil {
		a.events = map[string][]map[string]interface{}{}
	}
	m, _ := payload.(map[string]interface{})
	a.events[step] = append(a.events[step], m)
}
func (a *hooksAuditor) Log(_ context.Context, step string, payload interface{}) {
	a.record(step, payload)
}
func (a *hooksAuditor) LogError(_ context.Context, step string, payload interface{}) {
	a.record(step, payload)
}

func writeUITestHooksRepo(t *testing.T) (repo, rel string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	if _, err := os.Stat(filepath.Join(uiTestHooksVendoredDir(), "node_modules", "typescript", "package.json")); err != nil {
		t.Skip("tools/js-ts-indexer/node_modules/typescript not installed (run npm install there)")
	}
	repo = t.TempDir()
	rel = "src/pages/HomePage.tsx"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(uiTestHooksSampleTSX), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, rel
}

func hooksConfig(enabled bool) *config.Config {
	cfg := &config.Config{}
	cfg.Generation.UITestHooksEnabled = enabled
	return cfg
}

// Enabled + compiling tree: ids are written, the compile step ran once, and the summary names
// the file so the operator can find the edit in the diff.
func TestApplyUITestHooks_appliesAndVerifies(t *testing.T) {
	repo, rel := writeUITestHooksRepo(t)
	sb := &hooksSandbox{ok: true}
	aud := &hooksAuditor{}
	out := applyUITestHooks(context.Background(), hooksConfig(true), repo, "typescript", sb, aud)
	if len(out.Files) != 1 || out.Files[0] != rel || out.Added < 3 || out.RolledBack {
		t.Fatalf("outcome = %+v", out)
	}
	if sb.calls != 1 {
		t.Fatalf("compile calls = %d, want 1", sb.calls)
	}
	b, _ := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	for _, want := range []string{`<main data-testid="home-page-root">`, `data-testid="home-page-heading-welcome"`, `data-testid="home-page-button-get-started"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in:\n%s", want, b)
		}
	}
	if len(aud.events["ui_test_hooks.applied"]) != 1 || len(aud.events["ui_test_hooks.summary"]) != 1 {
		t.Fatalf("events = %v", aud.events)
	}
	if files, _ := aud.events["ui_test_hooks.summary"][0]["files"].([]string); len(files) != 1 {
		t.Fatalf("summary files = %v", aud.events["ui_test_hooks.summary"][0]["files"])
	}
}

// A failed compile restores the original bytes and reports nothing applied.
func TestApplyUITestHooks_rollsBackWhenCompileFails(t *testing.T) {
	repo, rel := writeUITestHooksRepo(t)
	aud := &hooksAuditor{}
	out := applyUITestHooks(context.Background(), hooksConfig(true), repo, "typescript", &hooksSandbox{ok: false, output: "error TS2322"}, aud)
	if !out.RolledBack || len(out.Files) != 0 || out.Added != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	b, _ := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if string(b) != uiTestHooksSampleTSX {
		t.Fatalf("file must be restored; got:\n%s", b)
	}
	if len(aud.events["ui_test_hooks.rolled_back"]) != 1 || len(aud.events["ui_test_hooks.summary"]) != 0 {
		t.Fatalf("events = %v", aud.events)
	}
}

// Off by default: nothing is read, written or audited.
func TestApplyUITestHooks_disabledIsNoop(t *testing.T) {
	repo, rel := writeUITestHooksRepo(t)
	sb := &hooksSandbox{ok: true}
	aud := &hooksAuditor{}
	out := applyUITestHooks(context.Background(), hooksConfig(false), repo, "typescript", sb, aud)
	b, _ := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if string(b) != uiTestHooksSampleTSX || len(out.Files) != 0 || sb.calls != 0 || len(aud.events) != 0 {
		t.Fatalf("disabled pass must be a no-op: out=%+v calls=%d events=%v", out, sb.calls, aud.events)
	}
}
