// Package runner: the StepPlan — one description of what an evaluation step runs, for either
// sandbox target.
//
// Before this type there were two independent implementations of "what does compile run?", called
// from the same `switch` in Sandbox.Compile/Test/Coverage: the Docker path assembled argv from a
// toolchain profile, the local path from localBuildCommand / jsLocalCommand / the .NET entry
// resolver. Nothing compared them, so they drifted — different build-tool selection, different
// skip decisions, different environments, and in several places different OK-ness for the same
// repository.
//
// A StepPlan is the single answer to that question. It is produced by buildStepPlan (planner.go),
// which delegates to the SAME builders each path already uses, so the plan cannot describe
// something production does not do. TestStepPlanParity then builds a plan for both targets from
// identical inputs and asserts they match, minus an explicit and shrinking whitelist.
//
// CP30 did not make the two targets agree — it made their disagreement visible, enumerated, and
// impossible to add to silently. CP31 moved both executors onto the plan and converged restore,
// JS/Java/.NET command construction and coverage-report discovery; CP32–CP35 close the rest.
package runner

import (
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// Target selects which sandbox a plan describes.
type Target string

const (
	TargetLocal  Target = "local"
	TargetDocker Target = "docker"
)

// StepAction is what the sandbox should do with a step.
type StepAction string

const (
	// ActionRun: execute the argv for this step.
	ActionRun StepAction = "run"
	// ActionSkip: do not execute; report OK with Reason. A repo fact the fixer cannot repair
	// (no build script, no JaCoCo plugin, unsupported language).
	ActionSkip StepAction = "skip"
	// ActionFail: do not execute; report NOT OK with Reason. Reserved for a plan that cannot be
	// built at all, e.g. a C# repo where no .sln/.csproj can be resolved.
	ActionFail StepAction = "fail"
)

// StepDecision is the planner's verdict for one step.
type StepDecision struct {
	Action StepAction
	// Reason is the operator-facing summary for Skip and Fail; empty for Run.
	Reason string
}

func runStep() StepDecision               { return StepDecision{Action: ActionRun} }
func skipStep(reason string) StepDecision { return StepDecision{Action: ActionSkip, Reason: reason} }
func failStep(reason string) StepDecision { return StepDecision{Action: ActionFail, Reason: reason} }

// StepPlan is everything a sandbox target needs in order to run an evaluation, and everything the
// parity harness compares between targets.
type StepPlan struct {
	Target    Target
	Lang      string
	Toolchain profile.ToolchainID

	// Image is the OCI image for TargetDocker; always empty for TargetLocal, where the toolchain
	// comes from the host PATH. One of the four permanent structural differences (§1 rows in the
	// parity whitelist).
	Image string

	// Profile is the resolved toolchain profile for TargetDocker (and, from CP31 on, the local
	// ecosystems that read one). Not compared by the parity harness — Compile/Test/Coverage below
	// carry the final argv, which is what actually runs.
	Profile profile.ToolchainProfile

	// Restore is the dependency-restore argv, or nil when the ecosystem/target has none. Both
	// targets read it from the same toolchain profile (CP31).
	Restore []string

	// RestoreKey fingerprints the ecosystem and its dependency manifests. The executor restores at
	// most once per key per run (see restore.go): once-per-round rather than once-per-step, and
	// automatically invalidated when the fix loop edits a manifest.
	RestoreKey string

	// RestoreDecision is the verdict for the restore phase. Only ActionRun and ActionFail occur:
	// a restore argv that cannot be resolved (a C# tree with no .sln/.csproj) fails the step in
	// production, and is checked AFTER the step's own decision so the reported error is the same
	// one the pre-plan code surfaced.
	RestoreDecision StepDecision

	Compile  []string
	Test     []string
	Coverage []string

	// Env is per-step because it is not yet uniform: Docker sets CI=true on every step while local
	// adds it only on the test/coverage steps and never on compile. CP33 converges these, after
	// which one slice would do. Entries are the variables the sandbox ADDS, not the whole process
	// environment.
	Env map[evaluator.SandboxStep][]string

	// Decisions carries Skip/Fail verdicts. A step with no entry is ActionRun.
	Decisions map[evaluator.SandboxStep]StepDecision

	// CoverageReportPaths are repo-relative locations where a coverage report is expected, used to
	// build the coverage summary. One source for both targets (coverage_paths.go, CP31).
	CoverageReportPaths []string
}

// ArgvFor returns the argv recorded for a step. A plain lookup by design: the Coverage→Test
// fallback lives in the argv builders, so what a plan records is what it will run, and a skipped
// step cannot appear to carry another step's command.
func (p StepPlan) ArgvFor(step evaluator.SandboxStep) []string {
	switch step {
	case evaluator.StepCompile:
		return p.Compile
	case evaluator.StepTest, evaluator.StepTestE2E:
		return p.Test
	case evaluator.StepCoverage:
		return p.Coverage
	default:
		return nil
	}
}

// DecisionFor returns the verdict for a step, defaulting to Run.
func (p StepPlan) DecisionFor(step evaluator.SandboxStep) StepDecision {
	if d, ok := p.Decisions[step]; ok {
		return d
	}
	return runStep()
}

// EnvFor returns the environment for a step.
func (p StepPlan) EnvFor(step evaluator.SandboxStep) []string {
	return p.Env[step]
}

// planSteps is the set of steps a plan describes. Lint and Mutation are unconditional stubs on
// both targets, so there is nothing to plan for them.
var planSteps = []evaluator.SandboxStep{
	evaluator.StepCompile,
	evaluator.StepTest,
	evaluator.StepCoverage,
}
