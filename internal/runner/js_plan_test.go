package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

func jsPlan(t *testing.T, target string, files map[string]string, mutate func(*Sandbox)) StepPlan {
	t.Helper()
	repo := writeRepoTree(t, files, nil)
	sb := &Sandbox{Type: target, Timeout: "30s"}
	if mutate != nil {
		mutate(sb)
	}
	p, err := sb.buildStepPlan(repo, "typescript", "")
	if err != nil {
		t.Fatalf("buildStepPlan: %v", err)
	}
	return p
}

// D6: "nothing to run" is a skip with a named reason — never a failure, never a silent pass.
// This shape produced three different answers before: local FAILED, npm/yarn failed, and pnpm
// PASSED via --if-present, so the same repository could ship or be blocked depending on lockfile
// and sandbox.
func TestJSPlan_missingTestScriptSkipsOnBothTargets(t *testing.T) {
	files := map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`}
	for _, target := range []string{"local", "docker"} {
		d := jsPlan(t, target, files, nil).DecisionFor(evaluator.StepTest)
		if d.Action != ActionSkip {
			t.Errorf("%s: test Action = %q, want skip", target, d.Action)
		}
		if !strings.Contains(d.Reason, "no test script") {
			t.Errorf("%s: reason %q should name the missing script", target, d.Reason)
		}
	}
}

// Every package manager probes both script names. pnpm looked only for `test:coverage` while npm,
// yarn and local looked only for `coverage`, so an identical repo resolved a different script
// depending on which lockfile it shipped.
func TestJSPlan_coverageProbesBothScriptNames(t *testing.T) {
	tests := []struct {
		name     string
		scripts  string
		lockfile string
		wantCmd  string
	}{
		{"npm coverage", `"coverage":"jest --coverage"`, "", "npm run coverage"},
		{"npm test:coverage", `"test:coverage":"vitest run --coverage"`, "", "npm run test:coverage"},
		{"pnpm coverage", `"coverage":"jest --coverage"`, "pnpm-lock.yaml", "pnpm run coverage"},
		{"pnpm test:coverage", `"test:coverage":"vitest --coverage"`, "pnpm-lock.yaml", "pnpm run test:coverage"},
		{"yarn test:coverage", `"test:coverage":"jest --coverage"`, "yarn.lock", "yarn run test:coverage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"package.json": `{"scripts":{"test":"jest",` + tc.scripts + `}}`}
			if tc.lockfile != "" {
				files[tc.lockfile] = "x"
			}
			for _, target := range []string{"local", "docker"} {
				got := strings.Join(jsPlan(t, target, files, nil).Coverage, " ")
				if !strings.Contains(got, tc.wantCmd) {
					t.Errorf("%s: coverage argv %q should invoke %q", target, got, tc.wantCmd)
				}
			}
		})
	}
}

// No coverage script means the only thing left to run is the unit suite the test step already
// ran, producing no report. Same reasoning as the Java JaCoCo gate.
func TestJSPlan_noCoverageScriptSkips(t *testing.T) {
	files := map[string]string{"package.json": `{"scripts":{"build":"tsc","test":"jest"}}`}
	for _, target := range []string{"local", "docker"} {
		d := jsPlan(t, target, files, nil).DecisionFor(evaluator.StepCoverage)
		if d.Action != ActionSkip {
			t.Errorf("%s: coverage Action = %q, want skip", target, d.Action)
		}
	}
}

// The `--if-present` / `||` mechanism is gone. Its two halves made the Docker coverage step
// incapable of reporting a problem: nothing ran when the script was absent, and a failing script
// was swallowed by the fallback.
func TestJSPlan_noIfPresentOrFallbackRemains(t *testing.T) {
	files := map[string]string{
		"package.json": `{"scripts":{"build":"tsc","test":"jest","coverage":"jest --coverage"}}`,
	}
	for _, lock := range []string{"", "pnpm-lock.yaml", "yarn.lock"} {
		f := map[string]string{"package.json": files["package.json"]}
		if lock != "" {
			f[lock] = "x"
		}
		for _, target := range []string{"local", "docker"} {
			p := jsPlan(t, target, f, nil)
			for _, step := range planSteps {
				argv := strings.Join(p.ArgvFor(step), " ")
				if strings.Contains(argv, "--if-present") {
					t.Errorf("%s/%s %s: --if-present survives in %q", target, lock, step, argv)
				}
				if strings.Contains(argv, "||") {
					t.Errorf("%s/%s %s: || fallback survives in %q", target, lock, step, argv)
				}
			}
		}
	}
}

// Docker gains local's heuristics: it used to run `npm run build` unconditionally and fail here.
func TestJSPlan_nestFallbackAppliesOnBothTargets(t *testing.T) {
	files := map[string]string{"package.json": `{"dependencies":{"@nestjs/core":"10"},"scripts":{}}`}
	for _, target := range []string{"local", "docker"} {
		p := jsPlan(t, target, files, nil)
		if got := strings.Join(p.Compile, " "); !strings.Contains(got, "nest build") {
			t.Errorf("%s: compile argv %q should fall back to nest build", target, got)
		}
		if got := strings.Join(p.Test, " "); !strings.Contains(got, "nest test") {
			t.Errorf("%s: test argv %q should fall back to nest test", target, got)
		}
	}
}

func TestJSPlan_buildThatRunsStartSkipsCompile(t *testing.T) {
	files := map[string]string{"package.json": `{"scripts":{"build":"npm run start","start":"ng serve"}}`}
	for _, target := range []string{"local", "docker"} {
		d := jsPlan(t, target, files, nil).DecisionFor(evaluator.StepCompile)
		if d.Action != ActionSkip {
			t.Errorf("%s: compile Action = %q, want skip", target, d.Action)
		}
	}
	// A configured compile_command of the same shape is no more able to compile.
	d := jsPlan(t, "docker", files, func(s *Sandbox) { s.CompileCommand = "npm run build" }).
		DecisionFor(evaluator.StepCompile)
	if d.Action != ActionSkip {
		t.Errorf("configured `npm run build`: Action = %q, want skip", d.Action)
	}
}

// Local gains corepack and node_modules/.bin, which were Docker-only.
func TestJSPlan_shellWrappingIsIdenticalOnBothTargets(t *testing.T) {
	files := map[string]string{
		"package.json":   `{"scripts":{"build":"tsc","test":"jest"}}`,
		"pnpm-lock.yaml": "x",
	}
	local := jsPlan(t, "local", files, nil)
	docker := jsPlan(t, "docker", files, nil)
	for _, step := range planSteps {
		l, d := strings.Join(local.ArgvFor(step), " "), strings.Join(docker.ArgvFor(step), " ")
		if l != d {
			t.Errorf("%s: local %q vs docker %q", step, l, d)
		}
	}
	if got := strings.Join(local.Compile, " "); !strings.Contains(got, "corepack enable") ||
		!strings.Contains(got, "node_modules/.bin") {
		t.Errorf("local compile %q should carry corepack and node_modules/.bin", got)
	}
}

// general.build.toolchain pins the package manager on both targets (§6): the image/version axis stays
// Docker-only, but which package manager runs must not depend on the sandbox.
func TestJSPlan_evalProfilePinsThePackageManagerOnBothTargets(t *testing.T) {
	files := map[string]string{"package.json": `{"scripts":{"build":"tsc","test":"jest"}}`}
	for _, target := range []string{"local", "docker"} {
		p := jsPlan(t, target, files, func(s *Sandbox) { s.EvalProfile = "typescript-yarn" })
		if p.Toolchain != profile.TypeScriptYarn {
			t.Errorf("%s: Toolchain = %q, want typescript-yarn", target, p.Toolchain)
		}
		if got := strings.Join(p.Test, " "); !strings.Contains(got, "yarn test") {
			t.Errorf("%s: test argv %q should use yarn", target, got)
		}
	}
}

// A JS repo with no package.json does nothing on either target, and must say so the same way.
func TestJSPlan_noPackageJSONSkipsIdentically(t *testing.T) {
	files := map[string]string{"README.md": "x"}
	local := jsPlan(t, "local", files, nil)
	docker := jsPlan(t, "docker", files, nil)
	for _, step := range planSteps {
		l, d := local.DecisionFor(step), docker.DecisionFor(step)
		if l.Action != ActionSkip || d.Action != ActionSkip {
			t.Errorf("%s: local %q docker %q, want skip on both", step, l.Action, d.Action)
		}
		if l.Reason != d.Reason {
			t.Errorf("%s: reasons differ — local %q vs docker %q", step, l.Reason, d.Reason)
		}
	}
}

// The Java JaCoCo gate applies on both targets: Docker appended jacoco:report unconditionally and
// failed with "No plugin found for prefix 'jacoco'" on a pom that never declared it.
func TestJavaPlan_jacocoGateAppliesOnBothTargets(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle")
	repo := writeRepoTree(t, map[string]string{"pom.xml": "<project/>"}, nil)
	for _, target := range []string{"local", "docker"} {
		sb := &Sandbox{Type: target, Timeout: "30s"}
		p, err := sb.buildStepPlan(repo, "java", "")
		if err != nil {
			t.Fatal(err)
		}
		d := p.DecisionFor(evaluator.StepCoverage)
		if d.Action != ActionSkip {
			t.Errorf("%s: coverage Action = %q, want skip without the JaCoCo plugin", target, d.Action)
		}
		if !strings.Contains(d.Reason, "JaCoCo") {
			t.Errorf("%s: reason %q should name JaCoCo", target, d.Reason)
		}
	}
}
