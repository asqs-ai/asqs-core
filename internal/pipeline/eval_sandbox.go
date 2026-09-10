package pipeline

import (
	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner"
)

// newEvalSandbox builds the evaluation sandbox and gives it the auditor, so it can record the step
// plan it resolved (runner.eval_plan_resolved).
//
// The nil check is the whole reason this is a function rather than two lines inline: assigning a
// nil evaluator.Auditor through a typed nil would produce a non-nil interface holding a nil value,
// which defeats the `s.Audit == nil` guard in auditEvalPlan and panics on first use.
func newEvalSandbox(cfg *config.Config, audit evaluator.Auditor) *runner.Sandbox {
	sb := runner.NewSandboxFromConfig(cfg)
	if audit != nil {
		sb.Audit = audit
	}
	return sb
}
