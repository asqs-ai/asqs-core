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
	absCwd := s.evalHostCwd(abs)
	p, err := profile.ResolveToolchain(absCwd, lang, s.EvalProfile, s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		return evaluator.StepResult{Step: stepEval, OK: true, Summary: fmt.Sprintf("skip (docker: %v)", err), Output: ""}
	}
	p = profile.ApplyCommandOverrides(p, s.CompileCommand, s.TestCommand)
	if strings.TrimSpace(imageOverride) != "" {
		p.Image = strings.TrimSpace(imageOverride)
	}
	s.logDockerEvalEnvOnce(p, abs)
	if stepEval == evaluator.StepCompile && (lang == "javascript" || lang == "typescript") {
		if !pathExists(filepath.Join(absCwd, "package.json")) {
			return evaluator.StepResult{Step: stepEval, OK: true, Summary: "skip (no package.json)"}
		}
	}

	argv := dockerArgvForStep(s, p, stepEval)
	if len(argv) == 0 {
		return evaluator.StepResult{Step: stepEval, OK: true, Summary: "skip (no command)"}
	}
	argv, dotnetErr := s.patchDotnetDockerEvalArgv(p, argv, abs, absCwd)
	if dotnetErr != nil {
		return evaluator.StepResult{Step: stepEval, OK: false, Summary: dotnetErr.Error(), Output: ""}
	}
	if p.ID == profile.CSharpDotnet && (stepEval == evaluator.StepTest || stepEval == evaluator.StepCoverage) {
		argv = ApplyDotnetTestDockerHangMitigationProps(argv)
		argv = ApplyDotnetTestDockerVSTestCLIArgs(argv, s.jobTimeout())
		argv = WrapDotnetDockerTestWithBuildServerShutdown(argv)
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

	// Restore phase (NuGet, Maven, npm, etc.): run whenever the profile defines it, before compile/test/coverage.
	// When docker_disable_offline_test is false, restore uses network=restore (usually bridge) and the main step
	// often uses network=none with --no-restore/--frozen so deps must be populated here. When docker_disable_offline_test
	// is true, the main step also uses bridge—but we still must restore first because compile argv is still --no-restore.
	if len(p.Restore) > 0 && (stepEval == evaluator.StepCompile || stepEval == evaluator.StepTest || stepEval == evaluator.StepCoverage) {
		restoreArgv := append([]string(nil), p.Restore...)
		restoreArgv, dotnetErr = s.patchDotnetDockerEvalArgv(p, restoreArgv, abs, absCwd)
		if dotnetErr != nil {
			return evaluator.StepResult{Step: stepEval, OK: false, Summary: dotnetErr.Error(), Output: ""}
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] step=%s phase=restore-deps argv=[%s] network=%s\n", label, strings.Join(restoreArgv, " "), netRestore)
		if _, rerr := s.runDockerJob(ctx, abs, p, restoreArgv, netRestore, dockerImageNeedsPlaywrightIPC(p.Image)); rerr != nil {
			fmt.Fprintf(os.Stderr, "  docker restore: %v (continuing)\n", rerr)
		}
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
	env := []string{"CI=true"}
	if p.ID == profile.CSharpDotnet {
		// Avoid MSBuild worker node reuse holding outputs open across interrupted runs; pairs with jobrunner
		// cidfile cleanup when the docker CLI is killed on timeout.
		// DOTNET_EnableDiagnostics=0 avoids the diagnostic IPC server keeping the process alive in some Linux/Docker setups.
		env = append(env, "NuGetAudit=false", "MSBUILDDISABLENODEREUSE=1", "DOTNET_EnableDiagnostics=0", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
	}
	env = append(env, s.DockerEvalExtraEnv...)
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
// relaxed MSBuild props, and Playwright/dotnet side-by-side runtime/SDK install prepended per container run.
func (s *Sandbox) patchDotnetDockerEvalArgv(p profile.ToolchainProfile, argv []string, absGitRoot, absCwd string) ([]string, error) {
	if p.ID != profile.CSharpDotnet {
		return argv, nil
	}
	var err error
	argv, err = ensureDotnetDockerInvocation(p, argv, absCwd)
	if err != nil {
		return nil, err
	}
	csprojAbs, err := ResolveCsprojAbsForDotnetDockerEval(absCwd, argv)
	if err != nil {
		return nil, err
	}
	argv = ApplyDotnetDockerMultiTargetFramework(argv, absCwd, csprojAbs, s.DotNetFallbackTargetFramework)
	argv, err = applyDotnetDockerTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
	if err != nil {
		return nil, err
	}
	argv = ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv)
	if dockerImageIsPlaywrightDotnet(p.Image) {
		install := PlaywrightDotnetDockerInstallShell(absGitRoot, csprojAbs, s.DotNetFallbackTargetFramework)
		if install != "" {
			argv = PrependShellSnippetToDockerCommand(argv, install)
		}
	}
	// Install the Artifacts Credential Provider plugin inside the container whenever ASQS
	// is injecting a VSS_NUGET_EXTERNAL_FEED_ENDPOINTS envelope. The stock dotnet SDK image
	// doesn't ship the plugin, and without it `dotnet restore` ignores the envelope and
	// fails against any private feed (NU1301).
	if DockerEvalEnvHasNuGetCredentialEnvelope(s.DockerEvalExtraEnv) {
		argv = PrependShellSnippetToDockerCommand(argv, NuGetCredentialProviderDockerInstallShell())
	}
	return argv, nil
}
