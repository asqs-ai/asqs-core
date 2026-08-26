package runner

import (
	"fmt"
	"strings"
	"testing"
)

// (Upstream's planner tests additionally include the plan-agrees-with-the-executor pair and the
// unknown-runner-type executor backstop; those land with CP34 and CP35 respectively.)

func TestPlanDocker_unresolvableToolchainSkipsEveryStep(t *testing.T) {
	sb := &Sandbox{Type: "docker", Timeout: "30s"}
	repo := writeRepo(t, map[string]string{"main.py": "print(1)"}, nil)

	plan, err := sb.buildStepPlan(repo, "python", "")
	if err != nil {
		t.Fatalf("buildStepPlan: %v", err)
	}
	if plan.Image != "" {
		t.Errorf("Image = %q, want empty so the env block is not logged", plan.Image)
	}
	for _, step := range planSteps {
		d := plan.DecisionFor(step)
		if d.Action != ActionSkip {
			t.Errorf("step %s: Action = %q, want skip", step, d.Action)
		}
		// CP34 unifies this wording with the local target's "skip (unsupported lang)".
		if !strings.HasPrefix(d.Reason, "skip (docker: ") {
			t.Errorf("step %s: reason %q, want the docker-side skip wording", step, d.Reason)
		}
	}
}

func TestPlan_unknownRunnerTypeIsAnError(t *testing.T) {
	sb := &Sandbox{Type: "dcoker"} // the typo from the plan doc
	if _, err := sb.buildStepPlan(t.TempDir(), "java", ""); err == nil {
		t.Fatal("an unrecognised runner.type must not produce a plan")
	}
}

// Image override is how the E2E pass swaps in a Playwright image; it must reach the plan.
func TestPlanDocker_imageOverrideApplies(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", "exit 0")

	plan, err := sb.buildStepPlan(repo, "java", "mcr.microsoft.com/playwright/java:v1.49.0-jammy")
	if err != nil {
		t.Fatalf("buildStepPlan: %v", err)
	}
	if plan.Image != "mcr.microsoft.com/playwright/java:v1.49.0-jammy" {
		t.Errorf("Image = %q, override not applied", plan.Image)
	}
}

// The restore memo is shared across the four clone-by-value entry points; a scoped-compile
// fallback re-planning the same repository must reuse the fingerprint, not re-restore.
func TestPlanRestoreKey_stableAcrossReplansOfTheSameRepo(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", "exit 0")
	p1, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	if p1.RestoreKey == "" || p1.RestoreKey != p2.RestoreKey {
		t.Fatalf("RestoreKey unstable across replans: %q vs %q", p1.RestoreKey, p2.RestoreKey)
	}
	if fmt.Sprintf("%v", p1.Restore) != fmt.Sprintf("%v", p2.Restore) {
		t.Fatalf("Restore argv unstable across replans: %v vs %v", p1.Restore, p2.Restore)
	}
}
