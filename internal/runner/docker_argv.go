package runner

import (
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// dockerArgvForStep returns argv for a Docker eval step from the (already resolved) toolchain profile.
// Config compile_command / test_command are applied in profile.ApplyCommandOverrides before this runs.
func dockerArgvForStep(_ *Sandbox, p profile.ToolchainProfile, step evaluator.SandboxStep) []string {
	switch step {
	case evaluator.StepCompile:
		return append([]string(nil), p.Compile...)
	case evaluator.StepTest:
		return append([]string(nil), p.Test...)
	case evaluator.StepCoverage:
		if len(p.Coverage) > 0 {
			return append([]string(nil), p.Coverage...)
		}
		return append([]string(nil), p.Test...)
	default:
		return nil
	}
}
