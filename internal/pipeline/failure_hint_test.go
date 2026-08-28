package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
)

// The read path is what made retrieval.failure_hint_file mean anything: before CP36 the configured
// path reached PlanOptions.FailureHintFile and nothing ever opened it, so failure-localized
// retrieval was unreachable from configuration no matter what an operator set.
func TestFailureHint_explicitPathIsRead(t *testing.T) {
	root := t.TempDir()
	writeAsqs(t, root, ".asqs/ci.log", "  error: cannot find symbol OrderService  ")

	cfg := &config.Config{}
	cfg.Retrieval.FailureHintFile = ".asqs/ci.log"

	rel := failureHintReadRelPath(cfg)
	if rel != ".asqs/ci.log" {
		t.Fatalf("read path = %q, want the configured file", rel)
	}
	got := loadFailureHintFromRepoFile(root, rel)
	if got != "error: cannot find symbol OrderService" {
		t.Errorf("hint = %q; want the trimmed file contents", got)
	}
}

// A hint file an operator never configured must not steer a run. The default path is read only when
// persistence is what put it there — otherwise a stale log left in a workspace would silently bias
// planning for every future run, with nothing in the config to explain it.
func TestFailureHint_defaultPathOnlyWithPersistence(t *testing.T) {
	if rel := failureHintReadRelPath(&config.Config{}); rel != "" {
		t.Errorf("read path = %q with nothing configured; want none", rel)
	}
	cfg := &config.Config{}
	cfg.Retrieval.PersistLastEvalFailure = true
	if rel := failureHintReadRelPath(cfg); rel != defaultLastEvalFailureHintRelPath {
		t.Errorf("read path = %q with persistence on; want the default", rel)
	}
	// An explicit path always beats the default, even with persistence on: that is how a CI job
	// points the planner at a build log it already produced.
	cfg.Retrieval.FailureHintFile = "build/last.log"
	if rel := failureHintReadRelPath(cfg); rel != "build/last.log" {
		t.Errorf("read path = %q; the explicit path must win", rel)
	}
}

// The hint file path is operator-controlled, so it is untrusted as far as this reader is concerned.
func TestFailureHint_readRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, bad := range []string{"../secret.txt", ".asqs/../../secret.txt", ""} {
		if got := loadFailureHintFromRepoFile(root, bad); got != "" {
			t.Errorf("path %q read %q from outside the repository", bad, got)
		}
	}
}

// Every read failure is the same answer — plan without a hint — because a run must not fail because
// a log file an operator mentioned has gone missing.
func TestFailureHint_missingFileIsNotAnError(t *testing.T) {
	if got := loadFailureHintFromRepoFile(t.TempDir(), ".asqs/nope.log"); got != "" {
		t.Errorf("missing file returned %q", got)
	}
	if got := loadFailureHintFromRepoFile("", ".asqs/x.log"); got != "" {
		t.Errorf("empty repo path returned %q", got)
	}
}

// Only compile/test/e2e failures become a hint. A lint or coverage failure says nothing about which
// production code the tests got wrong, which is the only question the hint is used to answer.
func TestFailureHint_rendersOnlyExecutionFailures(t *testing.T) {
	steps := []evaluator.StepResult{
		{Step: evaluator.StepCompile, OK: false, Summary: "compile failed", Output: "Order.java:12 cannot find symbol"},
		{Step: evaluator.StepTest, OK: true, Summary: "tests passed", Output: "all green"},
		{Step: evaluator.StepLint, OK: false, Summary: "lint failed", Output: "unused import"},
		{Step: evaluator.StepTestE2E, OK: false, Summary: "e2e failed", Output: "timeout waiting for #checkout"},
	}
	got := evalFailureHintFromSteps(steps)

	for _, want := range []string{"cannot find symbol", "timeout waiting for #checkout", "[compile]", "[test_e2e]"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint omits %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unused import") {
		t.Error("a lint failure reached the hint; it cannot tell the planner which code the tests got wrong")
	}
	if strings.Contains(got, "all green") {
		t.Error("a PASSING step reached the hint")
	}
}

// A green run must REMOVE the file. Leaving it would hand the next run a hint describing a failure
// that no longer exists, localizing retrieval on code that is already fixed — worse than no hint.
func TestFailureHint_stableRunClearsTheHint(t *testing.T) {
	root := t.TempDir()
	hint := writeAsqs(t, root, defaultLastEvalFailureHintRelPath, "old failure")
	cfg := &config.Config{}
	cfg.Retrieval.PersistLastEvalFailure = true

	persistLastEvalFailureHint(root, cfg, true, []evaluator.StepResult{
		{Step: evaluator.StepTest, OK: true, Summary: "passed"},
	})
	if exists(hint) {
		t.Error("a stable run left a stale hint behind")
	}
}

func TestFailureHint_failingRunWritesTheHint(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Retrieval.PersistLastEvalFailure = true

	persistLastEvalFailureHint(root, cfg, false, []evaluator.StepResult{
		{Step: evaluator.StepCompile, OK: false, Summary: "compile failed", Output: "Order.java:12 cannot find symbol"},
	})
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(defaultLastEvalFailureHintRelPath)))
	if err != nil {
		t.Fatalf("hint not written: %v", err)
	}
	if !strings.Contains(string(b), "cannot find symbol") {
		t.Errorf("hint content = %q", b)
	}
	// Round-trip: what was written must be what the read half loads next run.
	if got := loadFailureHintFromRepoFile(root, failureHintReadRelPath(cfg)); !strings.Contains(got, "cannot find symbol") {
		t.Errorf("written hint does not round-trip through the reader: %q", got)
	}
}

// Off by default: with persistence unset nothing is written, whatever the run did.
func TestFailureHint_notWrittenWhenPersistenceIsOff(t *testing.T) {
	root := t.TempDir()
	persistLastEvalFailureHint(root, &config.Config{}, false, []evaluator.StepResult{
		{Step: evaluator.StepCompile, OK: false, Summary: "compile failed", Output: "boom"},
	})
	if exists(filepath.Join(root, filepath.FromSlash(defaultLastEvalFailureHintRelPath))) {
		t.Error("a hint was written with persist_last_eval_failure off")
	}
}

// Test output is unbounded — a flaky suite emits megabytes — and this file is read back into a
// PROMPT next run, so an unbounded write is an unbounded prompt.
func TestFailureHint_writeIsBounded(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Retrieval.PersistLastEvalFailure = true

	persistLastEvalFailureHint(root, cfg, false, []evaluator.StepResult{
		{Step: evaluator.StepTest, OK: false, Summary: "failed", Output: strings.Repeat("x", 2*maxPersistedFailureHintBytes)},
	})
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(defaultLastEvalFailureHintRelPath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPersistedFailureHintBytes+128 {
		t.Errorf("persisted %d bytes, want it capped near %d", len(b), maxPersistedFailureHintBytes)
	}
	if !strings.Contains(string(b), "truncated by persist_last_eval_failure") {
		t.Error("truncation is silent; a reader cannot tell the hint is partial")
	}
}

// The written path is operator-controlled too, and this half actually creates files.
func TestFailureHint_writeRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(filepath.Dir(root), "escaped-hint.log")
	t.Cleanup(func() { _ = os.Remove(victim) })

	cfg := &config.Config{}
	cfg.Retrieval.PersistLastEvalFailure = true
	cfg.Retrieval.FailureHintFile = "../escaped-hint.log"

	persistLastEvalFailureHint(root, cfg, false, []evaluator.StepResult{
		{Step: evaluator.StepCompile, OK: false, Summary: "failed", Output: "boom"},
	})
	if exists(victim) {
		t.Error("persistence wrote outside the repository root")
	}
}
