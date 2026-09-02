package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

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
		if d.Reason != unsupportedLangSkipReason {
			t.Errorf("step %s: reason %q, want the shared %q", step, d.Reason, unsupportedLangSkipReason)
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

// reLoggedStepArgv matches the line every step emits just before it executes:
//
//	[asqs-eval] step=<step> phase=main argv=[<argv>] ...
//
// Both targets print it (logLocalEvalStep for local, runDockerEvalWithImageOverride for docker),
// which makes it the one place a test can observe what the sandbox ACTUALLY ran without stubbing
// the execution layer.
var reLoggedStepArgv = regexp.MustCompile(`\[asqs-eval\] step=(\S+) phase=main argv=\[(.*?)\] (?:cwd|network)=`)

// loggedArgv extracts the argv the sandbox executed, keyed by the step label it logged under.
// Space-joined rather than a []string because the log line is itself space-joined; comparing the
// joined form keeps the test honest about what it can actually observe.
func loggedArgv(stderr string) map[string]string {
	out := map[string]string{}
	for _, m := range reLoggedStepArgv.FindAllStringSubmatch(stderr, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// The plan must describe what the local path really runs, not a parallel re-derivation of it.
// Each case executes the real step against stubbed tools and compares the logged argv with the
// plan's. If planLocal* ever branches differently from runLocal*, this fails.
func TestPlanLocal_agreesWithWhatTheLocalPathExecutes(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		exec  map[string]bool
		stub  []string
		lang  string
		steps []evaluator.SandboxStep
	}{
		{
			name:  "maven",
			files: map[string]string{"pom.xml": jacocoPom},
			stub:  []string{"mvn", "gradle"},
			lang:  "java",
			steps: []evaluator.SandboxStep{evaluator.StepCompile, evaluator.StepTest, evaluator.StepCoverage},
		},
		{
			name:  "gradle",
			files: map[string]string{"build.gradle": "plugins { id 'jacoco' }"},
			stub:  []string{"mvn", "gradle"},
			lang:  "java",
			steps: []evaluator.SandboxStep{evaluator.StepCompile, evaluator.StepTest, evaluator.StepCoverage},
		},
		{
			name: "npm",
			files: map[string]string{
				"package.json": `{"scripts":{"build":"tsc","test":"jest","coverage":"jest --coverage"}}`,
			},
			stub:  []string{"npm", "node"},
			lang:  "typescript",
			steps: []evaluator.SandboxStep{evaluator.StepCompile, evaluator.StepTest, evaluator.StepCoverage},
		},
		{
			name: "dotnet",
			files: map[string]string{
				"App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
			},
			stub:  []string{"dotnet"},
			lang:  "csharp",
			steps: []evaluator.SandboxStep{evaluator.StepCompile, evaluator.StepTest, evaluator.StepCoverage},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubToolsOnPATH(t, tc.stub...)
			repo := writeRepo(t, tc.files, tc.exec)
			sb := &Sandbox{Type: "local", Timeout: "30s"}

			plan, err := sb.buildStepPlan(repo, tc.lang, "")
			if err != nil {
				t.Fatalf("buildStepPlan: %v", err)
			}

			stderr := captureStderr(t, func() {
				ctx := context.Background()
				sb.Compile(ctx, repo, tc.lang)
				sb.Test(ctx, repo, tc.lang)
				sb.Coverage(ctx, repo, tc.lang)
			})
			ran := loggedArgv(stderr)

			for _, step := range tc.steps {
				want, logged := ran[string(step)]
				if !logged {
					// A step the plan skips legitimately never logs an argv line.
					if plan.DecisionFor(step).Action != ActionRun {
						continue
					}
					t.Errorf("step %s: plan says Run but the local path executed nothing\n%s", step, stderr)
					continue
				}
				if got := strings.Join(plan.ArgvFor(step), " "); got != want {
					t.Errorf("step %s argv disagrees:\n  plan     %q\n  executed %q", step, got, want)
				}
			}
		})
	}
}

// The docker path now reads its argv from the plan, so agreement is structural. This asserts the
// wiring actually holds end to end — the argv reaching `docker run` is the plan's.
func TestPlanDocker_agreesWithWhatTheDockerPathExecutes(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", "exit 0")

	plan, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatalf("buildStepPlan: %v", err)
	}

	stderr := captureStderr(t, func() {
		sb.Test(context.Background(), repo, "java")
	})
	ran := loggedArgv(stderr)

	// The docker path logs under its human label ("Tests"), not the step name.
	want, ok := ran["Tests"]
	if !ok {
		t.Fatalf("no main-phase argv logged:\n%s", stderr)
	}
	if got := strings.Join(plan.Test, " "); got != want {
		t.Errorf("docker test argv disagrees:\n  plan     %q\n  executed %q", got, want)
	}
}

func TestUnknownRunnerType_failsEveryStepInsteadOfPassing(t *testing.T) {
	sb := &Sandbox{Type: "dcoker", Timeout: "30s"}
	repo := writeRepo(t, map[string]string{"pom.xml": "<project/>"}, nil)
	ctx := context.Background()

	for _, res := range []evaluator.StepResult{
		sb.Compile(ctx, repo, "java"),
		sb.Test(ctx, repo, "java"),
		sb.Coverage(ctx, repo, "java"),
	} {
		if res.OK {
			t.Errorf("%s reported OK for an unrecognised runner.type", res.Step)
		}
		if !strings.Contains(res.Summary, "dcoker") {
			t.Errorf("%s summary %q should name the offending value", res.Step, res.Summary)
		}
		if strings.TrimSpace(res.Output) == "" {
			t.Errorf("%s left Output empty, which the evaluator treats as in-scope for the fixer", res.Step)
		}
	}
}

// The valid runner.type set lives in one place; config validation and the executor backstop must
// name the same two values, so a future type cannot be added to one and missed by the other.
func TestValidRunnerTypes_isTheSingleSource(t *testing.T) {
	if len(validRunnerTypes) != 2 || !validRunnerTypes["local"] || !validRunnerTypes["docker"] {
		t.Fatalf("validRunnerTypes = %v, want exactly {local, docker}", validRunnerTypes)
	}
	for typ := range validRunnerTypes {
		sb := &Sandbox{Type: typ}
		if _, err := sb.buildStepPlan(t.TempDir(), "java", ""); err != nil {
			t.Errorf("planner rejects valid runner.type %q: %v", typ, err)
		}
	}
}

// The JS/TS branch of planDocker applies the override at its own site, not the one the Java case
// above exercises, so it needs its own regression guard: asqs-go carried the same planner with only
// the Java coverage and the JS/TS branch silently dropped the override, planning the plain Node
// image for every Playwright run. Asserts Profile.Image as well as Image —
// runDockerEvalWithImageOverride runs the profile, and dockerImageNeedsPlaywrightIPC reads
// Profile.Image to decide on --ipc=host.
func TestPlanDocker_imageOverrideAppliesForJSTS(t *testing.T) {
	sb, _ := fakeDockerSandbox(t, "30s", "exit 0")
	repo := writeRepo(t, map[string]string{
		"package.json": `{"name":"x","scripts":{"test":"playwright test"}}`,
	}, nil)
	const want = "mcr.microsoft.com/playwright:v1.49.1-jammy"

	for _, lang := range []string{"typescript", "javascript"} {
		plan, err := sb.buildStepPlan(repo, lang, want)
		if err != nil {
			t.Fatalf("%s: buildStepPlan: %v", lang, err)
		}
		if plan.Image != want {
			t.Errorf("%s: Image = %q, want the Playwright override", lang, plan.Image)
		}
		if plan.Profile.Image != want {
			t.Errorf("%s: Profile.Image = %q, want the Playwright override (this is the image that runs)", lang, plan.Profile.Image)
		}
	}
}

// No override means the plain Node toolchain image, for every step that is not the E2E pass.
func TestPlanDocker_jsWithoutOverrideKeepsNodeImage(t *testing.T) {
	sb, _ := fakeDockerSandbox(t, "30s", "exit 0")
	repo := writeRepo(t, map[string]string{
		"package.json": `{"name":"x","scripts":{"test":"jest"}}`,
	}, nil)

	plan, err := sb.buildStepPlan(repo, "typescript", "")
	if err != nil {
		t.Fatalf("buildStepPlan: %v", err)
	}
	if strings.Contains(plan.Image, "playwright") {
		t.Errorf("Image = %q, want the Node toolchain image when no override is given", plan.Image)
	}
}
