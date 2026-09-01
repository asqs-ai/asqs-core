package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// The host/plan path must run the runner test_framework_bootstrap installed and verified, not the
// repository's own `test` script.
//
// Bootstrap writes its runner to `test:asqs` and deliberately never claims `scripts.test` when the
// package already has one — overwriting it destroyed `ng test` on Angular repos. On such a package
// `<pm> test` then runs whatever was already there, which on an Angular fixture was
// `echo "no unit/e2e runners configured"`: exit 0 in under 300 ms, with every generated test
// unexecuted.
func TestJSStepScript_runsTheBootstrapInstalledScript(t *testing.T) {
	s := &Sandbox{}
	plan := &StepPlan{Lang: "typescript"}
	meta := jsPackageMeta{
		PackageManager: "npm",
		HasTest:        true,
		Scripts: map[string]string{
			"test":      `echo "no unit/e2e runners configured in this ASQS fixture"`,
			"test:asqs": "jest",
		},
	}
	script, dec := s.jsStepScript(plan, meta, evaluator.StepTest)
	if dec.Action != ActionRun {
		t.Fatalf("decision = %+v, want run", dec)
	}
	if !strings.Contains(script, "npm run test:asqs") {
		t.Errorf("script = %q, want it to run the script bootstrap verified", script)
	}
}

// Without the script nothing changes: a package whose own runner was already complete is skipped by
// bootstrap before any package.json edit, so `<pm> test` stays the right question to ask.
func TestJSStepScript_withoutBootstrapScriptKeepsPmTest(t *testing.T) {
	s := &Sandbox{}
	plan := &StepPlan{Lang: "typescript"}
	meta := jsPackageMeta{
		PackageManager: "pnpm",
		HasTest:        true,
		Scripts:        map[string]string{"test": "jest"},
	}
	script, dec := s.jsStepScript(plan, meta, evaluator.StepTest)
	if dec.Action != ActionRun {
		t.Fatalf("decision = %+v, want run", dec)
	}
	if !strings.Contains(script, "pnpm test") || strings.Contains(script, "test:asqs") {
		t.Errorf("script = %q, want the historical invocation", script)
	}
}

// A configured general.build.test_command still wins over both: it is the operator's decision, and
// it is also the reason a fix applied only to the defaults can look like it does nothing.
func TestJSStepScript_configuredCommandStillWins(t *testing.T) {
	s := &Sandbox{TestCommand: "npm run my-suite"}
	plan := &StepPlan{Lang: "typescript"}
	meta := jsPackageMeta{
		PackageManager: "npm",
		HasTest:        true,
		Scripts:        map[string]string{"test": "jest", "test:asqs": "jest"},
	}
	script, dec := s.jsStepScript(plan, meta, evaluator.StepTest)
	if dec.Action != ActionRun {
		t.Fatalf("decision = %+v, want run", dec)
	}
	if !strings.Contains(script, "npm run my-suite") {
		t.Errorf("script = %q, want the configured command", script)
	}
}

// The bootstrap script is honoured even when the package has no `test` at all — that case used to
// skip the step outright with "no test script".
func TestJSStepScript_bootstrapScriptWithoutRepoTest(t *testing.T) {
	s := &Sandbox{}
	plan := &StepPlan{Lang: "typescript"}
	meta := jsPackageMeta{
		PackageManager: "npm",
		Scripts:        map[string]string{"test:asqs": "vitest run"},
	}
	script, dec := s.jsStepScript(plan, meta, evaluator.StepTest)
	if dec.Action != ActionRun {
		t.Fatalf("decision = %+v, want run rather than a skip", dec)
	}
	if !strings.Contains(script, "npm run test:asqs") {
		t.Errorf("script = %q", script)
	}
}
