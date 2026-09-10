package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// A configured test_command must not become the COVERAGE command on a package that declares no
// coverage script.
//
// jsStepScript takes the override branch before it reaches its own coverage case, so the
// deliberate `skip (no coverage script declared in package.json)` decision — whose comment says
// the only thing left to run is "the unit suite, which the test step already ran and which
// produces no report" — was unreachable whenever the override was set. The Java planner already
// gets this right: its JaCoCo skip (planner.go) returns before profileArgvForStep, so an override
// never reaches a coverage step that cannot produce a report.
//
// Observed in run an asqs-go NestJS run (NestJS): test and coverage resolved to the
// identical `npm run test:asqs`, the test step passed five times at ~4.2s, and the coverage step
// then ran the same command and FAILED — putting coverage=fail on an otherwise green run.
func TestJSStepScript_testOverrideDoesNotBecomeCoverage(t *testing.T) {
	dir := t.TempDir()
	writePlanFixtureFile(t, dir, "package.json", `{"name":"x","scripts":{"test":"jest","test:asqs":"jest"}}`)
	sb := &Sandbox{Type: "local", Timeout: "30m", TestCommand: "npm run test:asqs"}
	plan, err := sb.buildStepPlan(dir, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	cov := plan.DecisionFor(evaluator.StepCoverage)
	if cov.Action != ActionSkip {
		t.Fatalf("coverage action = %v (argv %q), want skip: the override cannot produce a report",
			cov.Action, strings.Join(plan.ArgvFor(evaluator.StepCoverage), " "))
	}
	if !strings.Contains(cov.Reason, "no coverage script") {
		t.Errorf("coverage reason = %q, want it to name the cause", cov.Reason)
	}
	// The override still owns the test step — that is what it is for.
	if got := strings.Join(plan.ArgvFor(evaluator.StepTest), " "); !strings.Contains(got, "npm run test:asqs") {
		t.Errorf("test argv = %q, want the override", got)
	}
}

// Where a coverage script DOES exist, the single-knob contract stands: the override controls the
// coverage step too, because that step can actually produce a report.
func TestJSStepScript_testOverrideStillAppliesWhenCoverageScriptExists(t *testing.T) {
	dir := t.TempDir()
	writePlanFixtureFile(t, dir, "package.json", `{"name":"x","scripts":{"test":"jest","coverage":"jest --coverage"}}`)
	sb := &Sandbox{Type: "local", Timeout: "30m", TestCommand: "npm run test:asqs"}
	plan, err := sb.buildStepPlan(dir, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.DecisionFor(evaluator.StepCoverage).Action; got != ActionRun {
		t.Fatalf("coverage action = %v, want run", got)
	}
	if got := strings.Join(plan.ArgvFor(evaluator.StepCoverage), " "); !strings.Contains(got, "npm run test:asqs") {
		t.Errorf("coverage argv = %q, want the override to apply", got)
	}
}

// With no override at all, nothing changes: the skip was already correct.
func TestJSStepScript_coverageStillSkipsWithoutAnOverride(t *testing.T) {
	dir := t.TempDir()
	writePlanFixtureFile(t, dir, "package.json", `{"name":"x","scripts":{"test":"jest"}}`)
	sb := &Sandbox{Type: "local", Timeout: "30m"}
	plan, err := sb.buildStepPlan(dir, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.DecisionFor(evaluator.StepCoverage).Action; got != ActionSkip {
		t.Fatalf("coverage action = %v, want skip", got)
	}
}
