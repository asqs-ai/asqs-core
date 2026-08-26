package runner

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// buildStepPlan resolves what this Sandbox would run for every evaluation step.
//
// Named buildStepPlan rather than BuildStepPlan(gitRootAbs, workSubpath, lang, cfg) because every
// input it needs is already on the Sandbox; taking them as parameters would only create a second
// place for them to disagree. The parity harness exploits the method form directly: it builds one
// Sandbox, flips Type, and plans twice.
//
// Delegation is the point. The Docker branch calls profile.ResolveToolchain, ApplyCommandOverrides,
// dockerArgvForStep and the same .NET patch chain that runDockerEvalWithImageOverride calls; the
// local branch calls localBuildCommand, jsLocalCommand and dotnetShellLineWithProject, which are
// the only places runLocalCompile/Test/Coverage obtain argv. Nothing here re-derives a command, so
// a plan cannot describe something the sandbox would not actually run.
//
// This is the CP30 state of the planner: it describes core's two targets AS THEY ARE, and the
// parity whitelist enumerates their disagreements. CP31–CP35 move both the executors and this
// planner together, shrinking the whitelist to the structural rows.
func (s *Sandbox) buildStepPlan(gitRootAbs, lang, imageOverride string) (StepPlan, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	abs, err := filepath.Abs(strings.TrimSpace(gitRootAbs))
	if err != nil || abs == "" {
		return StepPlan{}, fmt.Errorf("plan: invalid repo path %q: %w", gitRootAbs, err)
	}
	plan := StepPlan{
		Lang:      lang,
		Env:       map[evaluator.SandboxStep][]string{},
		Decisions: map[evaluator.SandboxStep]StepDecision{},
	}
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case string(TargetLocal):
		plan.Target = TargetLocal
		s.planLocal(&plan, s.evalHostCwd(abs))
	case string(TargetDocker):
		plan.Target = TargetDocker
		s.planDocker(&plan, abs, s.evalHostCwd(abs), imageOverride)
	default:
		// CP35 turns an unrecognised runner.type into a run failure. Until then production keeps
		// its "stub" behaviour; only the planner is strict, so nothing depends on this yet.
		return StepPlan{}, fmt.Errorf("plan: unknown runner type %q (want local or docker)", s.Type)
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// Docker — mirrors runDockerEvalWithImageOverride
// ---------------------------------------------------------------------------

func (s *Sandbox) planDocker(plan *StepPlan, absGitRoot, absCwd, imageOverride string) {
	p, err := profile.ResolveToolchain(absCwd, plan.Lang, s.EvalProfile,
		s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		// Production reports this exact skip per step invocation; CP34 unifies the wording with
		// the local target's.
		for _, step := range planSteps {
			plan.Decisions[step] = skipStep(fmt.Sprintf("skip (docker: %v)", err))
		}
		return
	}
	p = profile.ApplyCommandOverrides(p, s.CompileCommand, s.TestCommand)
	if v := strings.TrimSpace(imageOverride); v != "" {
		p.Image = v
	}
	plan.Toolchain = p.ID
	plan.Image = p.Image
	plan.Profile = p

	env := dockerJobEnv(p, s.DockerEvalExtraEnv)
	for _, step := range planSteps {
		plan.Env[step] = append([]string(nil), env...)
	}

	// Core's Docker executor runs the restore phase before EVERY step invocation (no memo until
	// CP31); the plan records the argv once, with the same patch chain production applies.
	plan.RestoreDecision = runStep()
	if len(p.Restore) > 0 {
		restore, rerr := s.patchDotnetDockerEvalArgv(p, append([]string(nil), p.Restore...), absGitRoot, absCwd)
		if rerr != nil {
			// Production fails the STEP when the restore argv cannot be resolved; it is not the
			// same as a restore that runs and exits non-zero, which stays best-effort.
			plan.RestoreDecision = failStep(rerr.Error())
		} else {
			plan.Restore = restore
		}
	}

	for _, step := range planSteps {
		plan.Decisions[step] = s.planDockerStep(plan, p, step, absGitRoot, absCwd)
	}
}

func (s *Sandbox) planDockerStep(plan *StepPlan, p profile.ToolchainProfile, step evaluator.SandboxStep, absGitRoot, absCwd string) StepDecision {
	if step == evaluator.StepCompile && isJSLang(plan.Lang) && !pathExists(filepath.Join(absCwd, "package.json")) {
		return skipStep("skip (no package.json)")
	}
	argv := dockerArgvForStep(s, p, step)
	if len(argv) == 0 {
		return skipStep("skip (no command)")
	}
	argv, err := s.patchDotnetDockerEvalArgv(p, argv, absGitRoot, absCwd)
	if err != nil {
		return failStep(err.Error())
	}
	if p.ID == profile.CSharpDotnet && (step == evaluator.StepTest || step == evaluator.StepCoverage) {
		argv = ApplyDotnetTestDockerHangMitigationProps(argv)
		argv = ApplyDotnetTestDockerVSTestCLIArgs(argv, s.jobTimeout())
		// `dotnet build-server shutdown` kills EVERY MSBuild/Roslyn node on the machine, not just
		// this step's. In a container that is the container; on a host it would reach a concurrent
		// run or the operator's IDE. §1 row 5 — machine-global blast radius is a permitted
		// difference, and one the local target must not acquire silently.
		argv = WrapDotnetDockerTestWithBuildServerShutdown(argv)
	}
	plan.setArgv(step, argv)
	return runStep()
}

// ---------------------------------------------------------------------------
// Local — mirrors runLocalCompile/Test/Coverage
// ---------------------------------------------------------------------------

func (s *Sandbox) planLocal(plan *StepPlan, absCwd string) {
	switch {
	case isCSharpLang(plan.Lang):
		plan.Toolchain = profile.CSharpDotnet
		s.planLocalDotnet(plan, absCwd)
	case isJSLang(plan.Lang):
		plan.Toolchain = detectLocalJSToolchain(absCwd)
		s.planLocalJS(plan, absCwd)
	case plan.Lang == "java":
		plan.Toolchain = detectLocalJavaToolchain(absCwd, s.BuildTool)
		s.planLocalJava(plan, absCwd)
		plan.CoverageReportPaths = localJavaCoverageReportPaths()
	default:
		for _, step := range planSteps {
			plan.Decisions[step] = skipStep("skip (unsupported lang)")
		}
	}
}

// planLocalJava mirrors runLocalCompile/Test/Coverage's Java branch, including its asymmetries: a
// compile or test that cannot resolve a command FAILS while a coverage that cannot is a SKIP, and
// the coverage step runs the plain "test" goal (there is no separate local coverage goal yet —
// CP31).
func (s *Sandbox) planLocalJava(plan *StepPlan, absCwd string) {
	for _, step := range planSteps {
		// runLocalCoverage passes the plain "test" goal to localBuildCommand — a "coverage" goal
		// would fall through to the compile arguments there. Only the JS path has a coverage goal.
		goal := "test"
		if step == evaluator.StepCompile {
			goal = "compile"
		}
		cmd, err := localBuildCommand(absCwd, goal, s.BuildTool, s.CompileCommand, s.TestCommand)
		if err != nil {
			if step == evaluator.StepCoverage {
				plan.Decisions[step] = skipStep("no build tool")
			} else {
				plan.Decisions[step] = failStep(err.Error())
			}
			continue
		}
		plan.setArgv(step, cmd.Args)
		plan.Decisions[step] = runStep()
	}
}

// planLocalJS mirrors runJSCompile/runJSTest/runJSCoverage, whose three error branches disagree
// with one another: compile turns a missing build script into a SKIP, coverage turns any
// resolution error into a SKIP, and test turns it into a FAIL. CP34 settles this.
func (s *Sandbox) planLocalJS(plan *StepPlan, absCwd string) {
	for _, step := range planSteps {
		goal := localGoalForCore(step)
		override := s.CompileCommand
		if step != evaluator.StepCompile {
			override = s.TestCommand
		}
		cmd, err := jsLocalCommand(absCwd, goal, override)
		if err != nil {
			switch {
			case step == evaluator.StepCompile && strings.Contains(err.Error(), "no build script"):
				plan.Decisions[step] = skipStep("skip (no build script)")
			case step == evaluator.StepCoverage:
				plan.Decisions[step] = skipStep("no test script")
			default:
				plan.Decisions[step] = failStep(err.Error())
			}
			continue
		}
		// runJSTest/runJSCoverage add CI=true (watch-mode kill switch); compile does not. The plan
		// records what the sandbox ADDS to the process environment.
		if step != evaluator.StepCompile {
			plan.Env[step] = []string{"CI=true"}
		}
		plan.setArgv(step, cmd.Args)
		plan.Decisions[step] = runStep()
	}
}

// planLocalDotnet mirrors runDotnetCompile/Test/Coverage: an explicit compile_command/test_command
// wins, else dotnetShellLineWithProject resolves the entry .sln/.csproj into a shell line; every
// step runs through `sh -c`. A resolution failure FAILS all three steps in production. CP31/CP32
// move local C# onto the shared toolchain profile.
func (s *Sandbox) planLocalDotnet(plan *StepPlan, absCwd string) {
	type stepSpec struct {
		step     evaluator.SandboxStep
		override string
		prefix   string
	}
	specs := []stepSpec{
		{evaluator.StepCompile, s.CompileCommand, "dotnet build -c Release"},
		{evaluator.StepTest, s.TestCommand, "dotnet test -c Release --no-build"},
		{evaluator.StepCoverage, s.TestCommand, `dotnet test -c Release --no-build --collect 'XPlat Code Coverage'`},
	}
	for _, sp := range specs {
		line := strings.TrimSpace(sp.override)
		if line == "" {
			var err error
			line, err = dotnetShellLineWithProject(absCwd, sp.prefix, s.DotNetFallbackTargetFramework)
			if err != nil {
				plan.Decisions[sp.step] = failStep(err.Error())
				continue
			}
		}
		// runDotnetTest/Coverage add CI=true; compile does not.
		if sp.step != evaluator.StepCompile {
			plan.Env[sp.step] = []string{"CI=true"}
		}
		plan.setArgv(sp.step, []string{"sh", "-c", line})
		plan.Decisions[sp.step] = runStep()
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (p *StepPlan) setArgv(step evaluator.SandboxStep, argv []string) {
	out := append([]string(nil), argv...)
	switch step {
	case evaluator.StepCompile:
		p.Compile = out
	case evaluator.StepTest, evaluator.StepTestE2E:
		p.Test = out
	case evaluator.StepCoverage:
		p.Coverage = out
	}
}

// localGoalForCore maps a sandbox step onto the goal string localBuildCommand and jsLocalCommand
// take. Note the Java coverage step maps to "test" for localBuildCommand (runLocalCoverage passes
// "test") while jsLocalCommand receives "coverage" — mirrored per call site above.
func localGoalForCore(step evaluator.SandboxStep) string {
	switch step {
	case evaluator.StepCompile:
		return "compile"
	case evaluator.StepCoverage:
		return "coverage"
	default:
		return "test"
	}
}

func isJSLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	}
	return false
}

func isCSharpLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		return true
	}
	return false
}

// detectLocalJSToolchain reports the package manager the local path would use. readJSPackageMeta
// is the same lockfile probe jsLocalCommand runs, so the two cannot disagree.
func detectLocalJSToolchain(absCwd string) profile.ToolchainID {
	switch readJSPackageMeta(absCwd).PackageManager {
	case "yarn":
		return profile.TypeScriptYarn
	case "pnpm":
		return profile.TypeScriptPNPM
	default:
		return profile.TypeScriptNPM
	}
}

// detectLocalJavaToolchain reports Maven vs Gradle the way localBuildCommand resolves it, at the
// family level (the wrapper-vs-binary axis is CP32's to remove and is not modelled here).
func detectLocalJavaToolchain(absCwd, buildTool string) profile.ToolchainID {
	switch strings.ToLower(strings.TrimSpace(buildTool)) {
	case "mvn", "mvnw":
		return profile.JavaMaven
	case "gradle", "gradlew":
		return profile.JavaGradle
	}
	if pathExists(filepath.Join(absCwd, "pom.xml")) {
		return profile.JavaMaven
	}
	if pathExists(filepath.Join(absCwd, "build.gradle")) || pathExists(filepath.Join(absCwd, "build.gradle.kts")) {
		return profile.JavaGradle
	}
	return profile.UnsupportedDocker
}

// localJavaCoverageReportPaths is the list coverageSummary scans; the two share this function so
// the plan and the executor cannot disagree about where a report may appear.
func localJavaCoverageReportPaths() []string {
	return []string{"target/site/jacoco/index.html", "build/reports/jacoco/test/html/index.html"}
}
