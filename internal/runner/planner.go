package runner

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/buildtool"
	"github.com/asqs/asqs-core/internal/config"
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
// Delegation is the point. The Docker branch calls profile.ResolveToolchain, ApplyCommandOverrides
// and the same .NET patch chain the executor applies; the local branch calls localBuildCommand and
// the shared JS planner. Since CP31 both executors RUN what the plan records — the plan is the
// single source of argv, restore and skip/fail decisions — so a plan cannot describe something
// the sandbox would not actually run.
//
// CP32–CP35 continue to converge the two targets (wrappers, env, policy, dead code), shrinking the
// parity whitelist to the structural rows.
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
		s.planLocal(&plan, abs, s.evalHostCwd(abs))
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
// Docker
// ---------------------------------------------------------------------------

func (s *Sandbox) planDocker(plan *StepPlan, absGitRoot, absCwd, imageOverride string) {
	// One JS planner for both targets (CP31), reached BEFORE the toolchain-resolution guard below.
	// A JS repo with no package.json makes ResolveToolchain fail, and bailing there produced a
	// plan with no toolchain, no restore and a differently-worded skip than local's — a divergence
	// in the very case where both targets do exactly nothing.
	if isJSLang(plan.Lang) {
		if p, err := profile.ResolveToolchain(absCwd, plan.Lang, s.EvalProfile,
			s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet); err == nil {
			if v := strings.TrimSpace(imageOverride); v != "" {
				p.Image = v
			}
			plan.Image = p.Image
			plan.Profile = p
		}
		s.planJS(plan, absCwd)
		return
	}
	p, err := profile.ResolveToolchain(absCwd, plan.Lang, s.EvalProfile,
		s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		// CP34 unifies this wording with the local target's unsupported-lang skip.
		for _, step := range planSteps {
			plan.Decisions[step] = skipStep(fmt.Sprintf("skip (docker: %v)", err))
		}
		return
	}
	// runner.build_tool selects Maven vs Gradle on BOTH targets (CP32). The Docker path ignored
	// the key entirely: profile.DetectToolchainID reads only the repo layout, so a repository
	// carrying both a pom.xml and a build.gradle ran Maven in the container while the local runner
	// obeyed `build_tool: gradle` and ran Gradle — the same config evaluating a different build
	// system depending on the sandbox.
	if id, changed := javaProfileForBuildTool(p.ID, s.BuildTool); changed {
		p = profile.BuiltinToolchain(id, s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	}
	p = profile.ApplyCommandOverrides(p, s.CompileCommand, s.TestCommand)
	if v := strings.TrimSpace(imageOverride); v != "" {
		p.Image = v
	}
	plan.Toolchain = p.ID
	plan.Image = p.Image
	plan.Profile = p
	plan.CoverageReportPaths = coverageReportPathsFor(p.ID)

	env := dockerJobEnv(p, s.DockerEvalExtraEnv)
	for _, step := range planSteps {
		plan.Env[step] = append([]string(nil), env...)
	}

	plan.RestoreDecision = runStep()
	plan.RestoreKey = restoreKeyFor(absCwd, p.ID, absCwd)
	if len(p.Restore) > 0 {
		restore, rerr := s.patchDotnetEvalArgv(p, append([]string(nil), p.Restore...), absGitRoot, absCwd, TargetDocker)
		restore = s.applyDotnetContainerProvisioning(p, restore, absGitRoot, absCwd)
		if rerr != nil {
			// Production fails the STEP when the restore argv cannot be resolved; it is not the
			// same as a restore that runs and exits non-zero, which stays best-effort.
			plan.RestoreDecision = failStep(rerr.Error())
		} else {
			plan.Restore = restore
		}
	}

	for _, step := range planSteps {
		plan.Decisions[step] = s.planProfileStep(plan, p, step, absGitRoot, absCwd, TargetDocker)
	}
}

// planProfileStep plans one step from a toolchain profile, for either target. Docker uses it for
// Java and C#; local uses it for C#, where the argv chain is identical apart from the
// container-provisioning prepends and the post-test build-server shutdown.
func (s *Sandbox) planProfileStep(plan *StepPlan, p profile.ToolchainProfile, step evaluator.SandboxStep, absGitRoot, absCwd string, target Target) StepDecision {
	if step == evaluator.StepCoverage && isJavaToolchain(p.ID) && !javaBuildFileDeclaresJaCoCo(absCwd) {
		// Without the plugin there is no report to produce, so the coverage step could only re-run
		// the suite the test step already ran — and Maven fails outright with "No plugin found for
		// prefix 'jacoco'". Local has always skipped; Docker appended jacoco:report regardless.
		return skipStep("skip (no JaCoCo plugin declared in the build file)")
	}
	argv := dockerArgvForStep(s, p, step)
	if len(argv) == 0 {
		return skipStep("skip (no command)")
	}
	argv, err := s.patchDotnetEvalArgv(p, argv, absGitRoot, absCwd, target)
	if err != nil {
		return failStep(err.Error())
	}
	if p.ID == profile.CSharpDotnet && (step == evaluator.StepTest || step == evaluator.StepCoverage) {
		argv = ApplyDotnetTestHangMitigationProps(argv)
		argv = ApplyDotnetTestVSTestCLIArgs(argv, s.jobTimeout())
		if target == TargetDocker {
			// `dotnet build-server shutdown` kills EVERY MSBuild/Roslyn node on the machine, not
			// just this step's. In a container that is the container; on a host it would reach a
			// concurrent run or the operator's IDE. §1 row 5 — machine-global blast radius is a
			// permitted difference, and one the local target must not acquire silently.
			argv = WrapDotnetTestWithBuildServerShutdown(argv)
		}
	}
	if target == TargetDocker {
		argv = s.applyDotnetContainerProvisioning(p, argv, absGitRoot, absCwd)
	}
	plan.setArgv(step, argv)
	return runStep()
}

// ---------------------------------------------------------------------------
// Local
// ---------------------------------------------------------------------------

func (s *Sandbox) planLocal(plan *StepPlan, absGitRoot, absCwd string) {
	switch {
	case isCSharpLang(plan.Lang):
		plan.Toolchain = profile.CSharpDotnet
		s.planLocalDotnet(plan, absGitRoot, absCwd)
		return
	case isJSLang(plan.Lang):
		s.planJS(plan, absCwd)
		return
	case plan.Lang == "java":
		plan.Toolchain = detectLocalJavaToolchain(absCwd, s.BuildTool)
		s.planLocalJava(plan, absCwd)
		plan.CoverageReportPaths = coverageReportPathsFor(plan.Toolchain)
	default:
		for _, step := range planSteps {
			plan.Decisions[step] = skipStep("skip (unsupported lang)")
		}
		return
	}
	// CP31: local restores too, from the SAME toolchain profile the Docker target reads, so the
	// two argv cannot drift.
	plan.RestoreDecision = runStep()
	plan.Restore = restoreArgvFor(plan.Toolchain)
	plan.RestoreKey = restoreKeyFor(absCwd, plan.Toolchain, absCwd)
}

// planLocalJava mirrors the local Java executor's semantics, including its asymmetry: a compile or
// test that cannot resolve a command FAILS, while a coverage that cannot is a SKIP.
func (s *Sandbox) planLocalJava(plan *StepPlan, absCwd string) {
	for _, step := range planSteps {
		// Recorded before the decision so a skipped step still reports the environment it would
		// have used. newLocalBuildCmd applies the same base env to the process it builds (CP33).
		plan.Env[step] = stepEnv(plan.Toolchain, TargetLocal, nil)
		cmd, err := localBuildCommand(absCwd, localGoalFor(step), s.BuildTool, s.CompileCommand, s.TestCommand)
		if err != nil {
			plan.Decisions[step] = localJavaFailure(step, err)
			continue
		}
		argv := cmd.Args
		if plan.Toolchain == profile.JavaMaven && s.credentialFor(config.EcosystemMaven) != "" {
			// The container gets the settings.xml mounted at Maven's default location; a host has
			// no mount table, so the path must be named (§1 row 4).
			argv = applyLocalMavenSettings(argv, s.credentialFor(config.EcosystemMaven))
		}
		plan.setArgv(step, argv)
		plan.Decisions[step] = runStep()
	}
}

func localJavaFailure(step evaluator.SandboxStep, err error) StepDecision {
	if step != evaluator.StepCoverage {
		return failStep(err.Error())
	}
	if errors.Is(err, errLocalCoverageUnavailable) {
		return skipStep("skip (no JaCoCo plugin declared in the build file)")
	}
	return skipStep("no build tool")
}

// planLocalDotnet plans local C# from the SAME toolchain profile the Docker target uses, and runs
// the same argv chain (CP31/U2b). Previously it built a bare `sh -c "dotnet build -c Release
// <proj>"` and skipped the whole chain — multitarget pin, MSBuild props, VSTest session timeout —
// and skipped even the fallback TFM whenever compile_command/test_command was set.
//
// This could not land before the restore stage existed: the Docker compile argv carries
// `--no-restore`, and until local had a restore stage that implicit restore was the only thing
// writing project.assets.json.
func (s *Sandbox) planLocalDotnet(plan *StepPlan, absGitRoot, absCwd string) {
	p, err := profile.ResolveToolchain(absCwd, plan.Lang, s.EvalProfile,
		s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		for _, step := range planSteps {
			plan.Decisions[step] = skipStep(fmt.Sprintf("skip (%v)", err))
		}
		return
	}
	p = profile.ApplyCommandOverrides(p, s.CompileCommand, s.TestCommand)
	// Image stays empty: on a host the toolchain comes from PATH (§1 row 1).
	plan.Profile = p
	plan.CoverageReportPaths = coverageReportPathsFor(p.ID)

	plan.RestoreDecision = runStep()
	plan.RestoreKey = restoreKeyFor(absCwd, p.ID, absCwd)
	if len(p.Restore) > 0 {
		restore, rerr := s.patchDotnetEvalArgv(p, append([]string(nil), p.Restore...), absGitRoot, absCwd, TargetLocal)
		if rerr != nil {
			plan.RestoreDecision = failStep(rerr.Error())
		} else {
			plan.Restore = restore
		}
	}
	for _, step := range planSteps {
		plan.Env[step] = append(stepEnv(p.ID, TargetLocal, nil), s.localCredentialEnv(p.ID)...)
		plan.Decisions[step] = s.planProfileStep(plan, p, step, absGitRoot, absCwd, TargetLocal)
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

// localGoalFor maps a sandbox step onto the goal string localBuildCommand takes.
func localGoalFor(step evaluator.SandboxStep) string {
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

func isJavaToolchain(id profile.ToolchainID) bool {
	switch id {
	case profile.JavaMaven, profile.JavaMaven11, profile.JavaMaven21,
		profile.JavaGradle, profile.JavaGradle11, profile.JavaGradle21:
		return true
	}
	return false
}

// javaProfileForBuildTool swaps a Java toolchain profile onto the family runner.build_tool asks
// for, preserving the JDK variant that eval_profile selected. It reports false when the profile is
// not a Java one or build_tool expresses no preference, so C#/JS profiles and `auto` are untouched.
func javaProfileForBuildTool(current profile.ToolchainID, buildTool string) (profile.ToolchainID, bool) {
	canonical, _, ok := buildtool.Canonicalize(buildTool)
	if !ok || canonical == "auto" {
		return current, false
	}
	wantGradle := canonical == "gradle"
	var maven, gradle profile.ToolchainID
	switch current {
	case profile.JavaMaven, profile.JavaGradle:
		maven, gradle = profile.JavaMaven, profile.JavaGradle
	case profile.JavaMaven11, profile.JavaGradle11:
		maven, gradle = profile.JavaMaven11, profile.JavaGradle11
	case profile.JavaMaven21, profile.JavaGradle21:
		maven, gradle = profile.JavaMaven21, profile.JavaGradle21
	default:
		return current, false
	}
	want := maven
	if wantGradle {
		want = gradle
	}
	return want, want != current
}
