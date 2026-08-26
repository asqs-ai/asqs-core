package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/jobrunner"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// runDockerEval runs one evaluation step in an ephemeral container (toolchain profile).
func (s *Sandbox) runDockerEval(ctx context.Context, repoPath, lang, step string, label string) evaluator.StepResult {
	return s.runDockerEvalWithImageOverride(ctx, repoPath, lang, step, label, "")
}

// runDockerEvalWithImageOverride is like runDockerEval but replaces the toolchain image when override is non-empty (e.g. Playwright image for JS E2E pass).
func (s *Sandbox) runDockerEvalWithImageOverride(ctx context.Context, repoPath, lang, step, label, imageOverride string) evaluator.StepResult {
	stepEval := evaluator.SandboxStep(step)
	if stepEval != evaluator.StepCompile && stepEval != evaluator.StepTest && stepEval != evaluator.StepCoverage {
		return evaluator.StepResult{Step: stepEval, OK: true, Summary: "skip"}
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	abs, err := filepath.Abs(strings.TrimSpace(repoPath))
	if err != nil || abs == "" {
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: "invalid repo path", Output: ""}
	}
	// Since CP31 the step plan is the single source of argv, restore and skip/fail decisions —
	// the same plan the parity harness compares and the local target executes.
	plan, err := s.buildStepPlan(abs, lang, imageOverride)
	if err != nil {
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: err.Error(), Output: ""}
	}
	p := plan.Profile
	s.logDockerEvalEnvOnce(p, abs)

	switch dec := plan.DecisionFor(stepEval); dec.Action {
	case ActionSkip:
		return evaluator.StepResult{Step: stepEval, OK: true, Summary: dec.Reason, Output: ""}
	case ActionFail:
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: dec.Reason, Output: ""}
	}
	argv := plan.ArgvFor(stepEval)
	if len(argv) == 0 {
		return evaluator.StepResult{Step: stepEval, OK: true, Summary: "skip (no command)"}
	}

	netRestore := strings.TrimSpace(s.JobNetworkRestore)
	if netRestore == "" {
		netRestore = "bridge"
	}
	netTest := strings.TrimSpace(s.JobNetworkTest)
	if netTest == "" {
		netTest = "none"
	}
	if s.DockerDisableOfflineTest {
		netTest = netRestore
	}

	// Restore phase (NuGet, Maven, npm, etc.): before the step, at most once per manifest
	// fingerprint (see restore.go). Previously this ran before EVERY step invocation — compile,
	// test, coverage, the E2E pass and each scoped-compile fallback: five to six installs per
	// fix-loop iteration. The memo lives on the shared run state so clones share it, and the
	// fingerprint key means a fixer edit to a manifest re-restores automatically. Restore uses
	// network=restore (usually bridge) because the main step often runs network=none with
	// --no-restore/--frozen, so dependencies must be populated here.
	if plan.RestoreDecision.Action == ActionFail {
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: plan.RestoreDecision.Reason, Output: ""}
	}
	if len(plan.Restore) > 0 {
		s.runState().restoreOnce(plan.RestoreKey, func() {
			fmt.Fprintf(os.Stderr, "[asqs-eval] step=%s phase=restore-deps argv=[%s] network=%s\n", label, strings.Join(plan.Restore, " "), netRestore)
			if _, rerr := s.runDockerJob(ctx, abs, p, plan.Restore, netRestore, dockerImageNeedsPlaywrightIPC(p.Image)); rerr != nil {
				fmt.Fprintf(os.Stderr, "  docker restore: %v (continuing)\n", rerr)
			}
		})
	}

	network := netTest
	if s.DockerDisableOfflineTest {
		network = netRestore
	}
	fmt.Fprintf(os.Stderr, "[asqs-eval] step=%s phase=main argv=[%s] network=%s\n", label, strings.Join(argv, " "), network)
	res, runErr := s.runDockerJob(ctx, abs, p, argv, network, dockerImageNeedsPlaywrightIPC(p.Image))
	out := res.CombinedOutput
	if runErr != nil && res.ExitCode == 0 {
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: runErr.Error(), Output: out}
	}
	ok := res.ExitCode == 0
	if !ok && stepEval == evaluator.StepTest && (lang == "javascript" || lang == "typescript") && jsTestOutputSummaryShowsZeroFailures(out) {
		ok = true
	}
	summary := label + " ok"
	if !ok {
		summary = firstLines(out, 5)
		if summary == "" {
			summary = "failed"
		}
	} else if res.ExitCode != 0 && stepEval == evaluator.StepTest && (lang == "javascript" || lang == "typescript") && strings.Contains(summary, " ok") {
		summary = label + " ok (summary all passed; exit code ignored)"
	}
	if ok {
		fmt.Fprintf(os.Stderr, "  %s (docker): ok.\n", label)
	} else {
		fmt.Fprintf(os.Stderr, "  %s (docker): failed. %s\n", label, firstLines(summary, 2))
	}
	return evaluator.StepResult{Step: stepEval, OK: ok, Summary: summary, Output: out}
}

func (s *Sandbox) runDockerJob(ctx context.Context, hostWorkDir string, p profile.ToolchainProfile, command []string, network string, ipcHost bool) (jobrunner.JobResult, error) {
	return s.runDockerJobWithTimeout(ctx, hostWorkDir, p, command, network, ipcHost, 0)
}

// runDockerJobWithTimeout runs one docker job; if jobTimeout is 0, uses sandbox job timeout from config.
func (s *Sandbox) runDockerJobWithTimeout(ctx context.Context, hostWorkDir string, p profile.ToolchainProfile, command []string, network string, ipcHost bool, jobTimeout time.Duration) (jobrunner.JobResult, error) {
	t := s.jobTimeout()
	if jobTimeout > 0 {
		t = jobTimeout
	}
	env := dockerJobEnv(p, s.DockerEvalExtraEnv)
	spec := jobrunner.JobSpec{
		Image:          p.Image,
		HostWorkDir:    hostWorkDir,
		Workdir:        s.dockerContainerWorkdir(),
		Command:        command,
		Timeout:        t,
		Memory:         strings.TrimSpace(s.JobMemory),
		CPUs:           s.JobCPUs,
		PidsLimit:      s.JobPidsLimit,
		NetworkMode:    network,
		Env:            env,
		DockerBinary:   s.dockerBin(),
		ReadonlyRootfs: s.JobReadonlyRootfs,
		CacheMounts:    s.cacheMountsForProfile(p),
		IpcHost:        ipcHost,
	}
	return (&jobrunner.DockerRunner{Docker: s.dockerBin()}).Run(ctx, spec)
}

// stepEnv is the environment an evaluation step sets explicitly, on either target (CP33).
//
// One source for both, so a variable cannot reach a container and quietly miss the host. The
// .NET additions avoid MSBuild worker-node reuse holding outputs open across interrupted runs —
// pairing with jobrunner's cidfile cleanup when the CLI is killed on timeout — and
// DOTNET_EnableDiagnostics=0 stops the diagnostic IPC server keeping the process alive on some
// Linux/Docker setups. None of that is container-specific; the host had simply never been given
// them, and local compile had not even been given CI=true.
//
// How the environment is DELIVERED still differs, and that is the permitted difference (§1): a
// container receives only these variables, while a host process receives them appended to
// os.Environ() — which is where its toolchain, PATH and ~/.m2/settings.xml come from.
//
// dockerExtra carries the NuGet credential envelope, which reaches a container as `-e`; the local
// target delivers the same variable through localCredentialEnv (credentials.go).
func stepEnv(id profile.ToolchainID, target Target, dockerExtra []string) []string {
	env := append([]string(nil), baseStepEnv()...)
	if id == profile.CSharpDotnet {
		env = append(env, "NuGetAudit=false", "MSBUILDDISABLENODEREUSE=1", "DOTNET_EnableDiagnostics=0", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
	}
	if target == TargetDocker {
		env = append(env, dockerExtra...)
	}
	return env
}

// baseStepEnv is what every step sets on every target: CI=true, so build plugins and watch-mode
// test runners behave as they would in CI rather than waiting for a terminal.
func baseStepEnv() []string { return []string{"CI=true"} }

// dockerJobEnv is the environment a Docker eval job runs with. Shared by runDockerJobWithTimeout
// and the step planner so the plan cannot disagree with what the container actually receives.
func dockerJobEnv(p profile.ToolchainProfile, extra []string) []string {
	return stepEnv(p.ID, TargetDocker, extra)
}

func (s *Sandbox) dockerBin() string {
	if strings.TrimSpace(s.DockerBinary) != "" {
		return strings.TrimSpace(s.DockerBinary)
	}
	return "docker"
}

func (s *Sandbox) jobTimeout() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s.Timeout))
	if err != nil || d <= 0 {
		return 15 * time.Minute
	}
	return d
}

func (s *Sandbox) cacheMountsForProfile(p profile.ToolchainProfile) []jobrunner.CacheMount {
	var m []jobrunner.CacheMount
	add := func(host, target string) {
		host = strings.TrimSpace(host)
		if host == "" || target == "" {
			return
		}
		m = append(m, jobrunner.CacheMount{Source: host, Target: target})
	}
	switch p.ID {
	case profile.JavaMaven, profile.JavaMaven11, profile.JavaMaven21:
		add(s.CacheMavenHost, "/root/.m2")
	case profile.JavaGradle, profile.JavaGradle11, profile.JavaGradle21:
		add(s.CacheGradleHost, "/root/.gradle")
	case profile.TypeScriptNPM:
		add(s.CacheNpmHost, "/root/.npm")
		add(s.CacheCypressHost, "/root/.cache/Cypress")
	case profile.TypeScriptPNPM:
		add(s.CacheNpmHost, "/root/.npm")
		add(s.CachePnpmHost, "/root/.local/share/pnpm/store")
		add(s.CacheCypressHost, "/root/.cache/Cypress")
	case profile.TypeScriptYarn:
		add(s.CacheNpmHost, "/root/.npm")
		add(s.CacheCypressHost, "/root/.cache/Cypress")
	case profile.CSharpDotnet:
		add(s.CacheNuGetHost, "/root/.nuget/packages")
	}
	// Private-registry credential files (Maven settings.xml / npm .npmrc) are ecosystem-gated so
	// they only appear on containers that can actually use them. A Java image doesn't need a
	// .npmrc and a Node image doesn't need a Maven settings.xml — keeping the mount surface
	// minimal avoids exposing secrets to containers that wouldn't have read them anyway.
	for _, extra := range s.DockerEvalExtraMounts {
		if !privateRegistryMountAppliesToProfile(extra.Target, p.ID) {
			continue
		}
		m = append(m, extra)
	}
	return m
}

// privateRegistryMountAppliesToProfile gates extra credential mounts to the toolchain profiles that
// actually consume them. The target path is the sole discriminator because the mount target is
// fixed by ecosystem convention (/root/.m2/settings.xml for Maven, /root/.npmrc for npm/yarn/pnpm).
func privateRegistryMountAppliesToProfile(target string, id profile.ToolchainID) bool {
	switch {
	case target == "/root/.m2/settings.xml":
		return id == profile.JavaMaven || id == profile.JavaMaven11 || id == profile.JavaMaven21
	case target == "/root/.npmrc":
		return id == profile.TypeScriptNPM || id == profile.TypeScriptPNPM || id == profile.TypeScriptYarn
	default:
		return false
	}
}

// patchDotnetDockerEvalArgv mirrors test_framework_bootstrap C# Docker fixes: multitarget TFM pin, fallback TFM,
// relaxed MSBuild props. Target-neutral since CP31: the local target applies the same chain, which
// is what makes the parity harness's claim about local C# true rather than aspirational.
func (s *Sandbox) patchDotnetEvalArgv(p profile.ToolchainProfile, argv []string, absGitRoot, absCwd string, target Target) ([]string, error) {
	if p.ID != profile.CSharpDotnet {
		return argv, nil
	}
	var err error
	argv, err = ensureDotnetInvocation(p, argv, absCwd)
	if err != nil {
		return nil, err
	}
	csprojAbs, err := ResolveCsprojAbsForDotnetEval(absCwd, argv)
	if err != nil {
		return nil, err
	}
	argv = ApplyDotnetMultiTargetFramework(argv, absCwd, csprojAbs, s.DotNetFallbackTargetFramework)
	argv, err = applyDotnetEvalTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
	if err != nil {
		return nil, err
	}
	argv = ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv)
	return argv, nil
}

// applyDotnetContainerProvisioning prepends the shell snippets that install software INTO the
// container: a side-by-side .NET runtime for the Playwright image, and the Artifacts credential
// provider the stock SDK image lacks (NU1301 against any private feed without it). Docker-only —
// the host equivalent is a preflight (CP33) — and applied LAST, after every argv transform:
// prepending earlier means the script no longer begins with `dotnet …`, and the MSBuild-property
// insertion is anchored at the start of the line.
func (s *Sandbox) applyDotnetContainerProvisioning(p profile.ToolchainProfile, argv []string, absGitRoot, absCwd string) []string {
	if p.ID != profile.CSharpDotnet {
		return argv
	}
	if dockerImageIsPlaywrightDotnet(p.Image) {
		if csprojAbs, err := ResolveCsprojAbsForDotnetEval(absCwd, argv); err == nil {
			if install := PlaywrightDotnetDockerInstallShell(absGitRoot, csprojAbs, s.DotNetFallbackTargetFramework); install != "" {
				argv = PrependShellSnippetToDockerCommand(argv, install)
			}
		}
	}
	if DockerEvalEnvHasNuGetCredentialEnvelope(s.DockerEvalExtraEnv) {
		argv = PrependShellSnippetToDockerCommand(argv, NuGetCredentialProviderDockerInstallShell())
	}
	return argv
}
