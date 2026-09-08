package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// asqs-go run api-cfc3279416a7d8c7d2690799800c7647: one generated test's unhandled rejection
// killed the Jest process from round 3 on, so only one suite ever reported. The Node flag applies
// to the Node toolchains' test steps only.
func TestJSStepExtraEnv(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "")
	for _, id := range []profile.ToolchainID{profile.TypeScriptNPM, profile.TypeScriptPNPM, profile.TypeScriptYarn} {
		for _, step := range []evaluator.SandboxStep{evaluator.StepTest, evaluator.StepTestE2E} {
			got := jsStepExtraEnv(id, step, TargetDocker)
			if len(got) != 1 || got[0] != "NODE_OPTIONS="+nodeUnhandledRejectionsFlag {
				t.Errorf("%s/%s: got %v", id, step, got)
			}
		}
		if got := jsStepExtraEnv(id, evaluator.StepCompile, TargetDocker); got != nil {
			t.Errorf("%s compile: got %v, want nothing", id, got)
		}
	}
	for _, id := range []profile.ToolchainID{profile.JavaMaven, profile.JavaGradle, profile.CSharpDotnet} {
		if got := jsStepExtraEnv(id, evaluator.StepTest, TargetDocker); got != nil {
			t.Errorf("%s: Node flag leaked into a non-Node toolchain: %v", id, got)
		}
	}
}

func TestJSStepExtraEnv_mergesOperatorNodeOptionsOnHost(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=4096")
	got := jsStepExtraEnv(profile.TypeScriptNPM, evaluator.StepTest, TargetLocal)
	if len(got) != 1 || !strings.HasPrefix(got[0], "NODE_OPTIONS=--max-old-space-size=4096 ") || !strings.HasSuffix(got[0], nodeUnhandledRejectionsFlag) {
		t.Fatalf("got %v", got)
	}
	t.Setenv("NODE_OPTIONS", "--unhandled-rejections=strict")
	if got := jsStepExtraEnv(profile.TypeScriptNPM, evaluator.StepTest, TargetLocal); got != nil {
		t.Fatalf("an operator choice must win, got %v", got)
	}
	// Containers see only what the runner sets, so the host value is irrelevant there.
	if got := jsStepExtraEnv(profile.TypeScriptNPM, evaluator.StepTest, TargetDocker); len(got) != 1 || got[0] != "NODE_OPTIONS="+nodeUnhandledRejectionsFlag {
		t.Fatalf("docker: got %v", got)
	}
}
