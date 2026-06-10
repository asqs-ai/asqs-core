package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/runner"
	"github.com/asqs/asqs-core/internal/runner/jobrunner"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// EphemeralDocker runs bootstrap toolchain commands in `docker run --rm` (same model as eval jobrunner).
type EphemeralDocker struct {
	dockerBin string
	image     string
	// hostAbs is the git repository root on the host (Docker bind mount source); it maps to /workspace in the container.
	hostAbs string
	// containerWorkdir is the initial working directory inside the container (e.g. /workspace or /workspace/projects/app).
	containerWorkdir string
	network          string
	timeout          time.Duration
	memory           string
	cpus             float64
	pidsLimit        int64
	mounts           []jobrunner.CacheMount
	readonly         bool
	ipcHost          bool
	// extraDockerEnv is appended to JobSpec.Env after CI=true (e.g. VSS_NUGET_EXTERNAL_FEED_ENDPOINTS).
	extraDockerEnv []string
}

// Image returns the OCI image used for bootstrap containers.
func (e *EphemeralDocker) Image() string {
	if e == nil {
		return ""
	}
	return e.image
}

func shellQuoteArg(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

// workspaceScriptPath maps a host path under the Docker volume root (git checkout root) to /workspace/... in the container.
func workspaceScriptPath(hostGitRoot, hostPath string) (string, error) {
	hostGitRoot = filepath.Clean(strings.TrimSpace(hostGitRoot))
	hostPath = filepath.Clean(strings.TrimSpace(hostPath))
	rel, err := filepath.Rel(hostGitRoot, hostPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("bootstrap: path %q is not under git root %q", hostPath, hostGitRoot)
	}
	if rel == "." {
		return "/workspace", nil
	}
	return "/workspace/" + filepath.ToSlash(rel), nil
}

// containerDockerWorkdir returns the POSIX workdir under /workspace for a scoped mono-repo folder (or /workspace when workspace equals git root).
func containerDockerWorkdir(hostGitRoot, workspaceAbs string) (string, error) {
	hostGitRoot = filepath.Clean(strings.TrimSpace(hostGitRoot))
	workspaceAbs = filepath.Clean(strings.TrimSpace(workspaceAbs))
	if hostGitRoot == "" || workspaceAbs == "" {
		return "", fmt.Errorf("bootstrap: empty git root or workspace for container workdir")
	}
	rel, err := filepath.Rel(hostGitRoot, workspaceAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("bootstrap: workspace %q must be under git root %q", workspaceAbs, hostGitRoot)
	}
	if rel == "." {
		return "/workspace", nil
	}
	return "/workspace/" + filepath.ToSlash(rel), nil
}

// joinShellArgs builds a single POSIX shell command line with safe quoting.
func joinShellArgs(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuoteArg(a))
	}
	return b.String()
}

// RunArgv runs argv[0] with argv[1:] in repo. If e is nil, uses the host exec; otherwise runs
// `sh -c` inside an ephemeral container with repo mounted at /workspace.
func RunArgv(ctx context.Context, e *EphemeralDocker, repo string, argv []string, extraEnv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("bootstrap: empty argv")
	}
	if e == nil {
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = repo
		c.Env = append(os.Environ(), extraEnv...)
		return c.CombinedOutput()
	}
	line := joinShellArgs(argv)
	return e.sh(ctx, line, extraEnv)
}

// RunArgvWithShellPrefix runs argv in Docker after optional shellPrefix in the same container (required when
// prefix installs tooling under /usr/share/dotnet — each bootstrap docker job is a fresh `docker run --rm`).
// shellPrefix must be a valid shell snippet without a trailing "&&". When e is nil, shellPrefix is ignored.
//
// For `dotnet` argv the Microsoft Artifacts Credential Provider install snippet is automatically
// prepended whenever the container has VSS_NUGET_EXTERNAL_FEED_ENDPOINTS in its extra env: the
// plugin is absent from the stock dotnet SDK image and the envelope is inert without it, so
// `dotnet restore` would fail against any private feed with NU1301.
func RunArgvWithShellPrefix(ctx context.Context, e *EphemeralDocker, repo string, argv []string, extraEnv []string, shellPrefix string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("bootstrap: empty argv")
	}
	if e == nil {
		return RunArgv(ctx, nil, repo, argv, extraEnv)
	}
	line := joinShellArgs(argv)
	if s := strings.TrimSpace(shellPrefix); s != "" {
		line = s + " && " + line
	}
	if bootstrapArgvIsDotnet(argv) && runner.DockerEvalEnvHasNuGetCredentialEnvelope(e.extraDockerEnv) {
		line = runner.NuGetCredentialProviderDockerInstallShell() + " && " + line
	}
	return e.sh(ctx, line, extraEnv)
}

// bootstrapArgvIsDotnet is true when argv[0] names the dotnet CLI (driver on PATH).
func bootstrapArgvIsDotnet(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(argv[0]), "dotnet")
}

func (e *EphemeralDocker) sh(ctx context.Context, script string, extraEnv []string) ([]byte, error) {
	env := []string{"CI=true", "NPM_CONFIG_YES=true", "DEBIAN_FRONTEND=noninteractive"}
	env = append(env, e.extraDockerEnv...)
	env = append(env, extraEnv...)
	wd := strings.TrimSpace(e.containerWorkdir)
	if wd == "" {
		wd = "/workspace"
	}
	spec := jobrunner.JobSpec{
		Image:          e.image,
		HostWorkDir:    e.hostAbs,
		Workdir:        wd,
		Command:        []string{"sh", "-c", script},
		Timeout:        e.timeout,
		Memory:         e.memory,
		CPUs:           e.cpus,
		PidsLimit:      e.pidsLimit,
		NetworkMode:    e.network,
		Env:            env,
		DockerBinary:   e.dockerBin,
		ReadonlyRootfs: e.readonly,
		IpcHost:        e.ipcHost,
		CacheMounts:    e.mounts,
	}
	dr := &jobrunner.DockerRunner{Docker: e.dockerBin}
	res, err := dr.Run(ctx, spec)
	out := []byte(res.CombinedOutput)
	if err != nil {
		return out, err
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("bootstrap docker: exit %d\n%s", res.ExitCode, truncate(string(out), 4000))
	}
	return out, nil
}

// BootstrapDockerImage selects the container image for ephemeral E2E bootstrap in Docker.
type BootstrapDockerImage int

const (
	// BootstrapDockerStandard uses eval profile images (e.g. node:20-bookworm for JS unit bootstrap, Maven/Gradle for Java).
	BootstrapDockerStandard BootstrapDockerImage = iota
	// BootstrapDockerPlaywrightJS uses mcr.microsoft.com/playwright (browsers + Node) for JS/TS E2E bootstrap in Docker.
	BootstrapDockerPlaywrightJS
	// BootstrapDockerPlaywrightJava uses mcr.microsoft.com/playwright/java (JDK + Maven + browsers + OS deps) for Java E2E bootstrap in Docker.
	BootstrapDockerPlaywrightJava
	// BootstrapDockerPlaywrightDotNet uses mcr.microsoft.com/playwright/dotnet (.NET SDK + browsers + OS deps) for C# E2E bootstrap in Docker.
	BootstrapDockerPlaywrightDotNet
)

// RunPackageManagerInstall runs npm / pnpm / yarn install in repo. In ephemeral Docker, prepends
// `corepack enable` so pnpm and Yarn work in slim Node and Playwright images.
//
// Node images often ship a Corepack build whose embedded signing keys lag the registry; pnpm/yarn
// then fail with "Cannot find matching keyid" when Corepack fetches release metadata. Upgrading
// Corepack via npm before enable avoids that (see Node/corepack issues around registry key rotation).
//
// For pnpm, passes --store-dir outside the repo (host: user cache; docker: /root/…/store) so bootstrap
// does not populate a repo-local .pnpm-store when the project .npmrc sets store-dir to a relative path.
func RunPackageManagerInstall(ctx context.Context, e *EphemeralDocker, repo string, pm PackageManager, allowLockfileChange, hasLock, mustSyncLockfile bool, extraEnv []string) ([]byte, error) {
	env := append([]string(nil), extraEnv...)
	pnpmStore := ""
	if pm == PMPnpm {
		var err error
		pnpmStore, err = BootstrapPnpmStorePath(e != nil)
		if err != nil {
			return nil, err
		}
		if e == nil {
			if err := os.MkdirAll(pnpmStore, 0755); err != nil {
				return nil, fmt.Errorf("bootstrap: mkdir pnpm store %q: %w", pnpmStore, err)
			}
		}
	}
	if mustSyncLockfile && pm == PMYarn {
		env = appendImmutableYarnEnv(env)
	}
	instName, instArgs := installCmd(pm, allowLockfileChange, hasLock, mustSyncLockfile, pnpmStore)
	argv := append([]string{instName}, instArgs...)
	if e == nil {
		return RunArgv(ctx, nil, repo, argv, env)
	}
	inner := joinShellArgs(argv)
	var script string
	switch pm {
	case PMPnpm, PMYarn:
		script = "set -e; npm install -g corepack@latest; corepack enable; " + inner
	default:
		script = "set -e; corepack enable 2>/dev/null || true; " + inner
	}
	return e.sh(ctx, script, env)
}

// appendImmutableYarnEnv sets YARN_ENABLE_IMMUTABLE_INSTALLS=false so Yarn Berry can refresh the
// lockfile after bootstrap edits package.json under CI=true.
func appendImmutableYarnEnv(extraEnv []string) []string {
	for _, kv := range extraEnv {
		if strings.HasPrefix(strings.TrimSpace(kv), "YARN_ENABLE_IMMUTABLE_INSTALLS=") {
			return extraEnv
		}
	}
	return append(append([]string(nil), extraEnv...), "YARN_ENABLE_IMMUTABLE_INSTALLS=false")
}

func normalizeLangForProfile(lang string) string {
	k := strings.ToLower(strings.TrimSpace(lang))
	switch k {
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	default:
		return k
	}
}

// shouldUseDockerBootstrap returns true when TS/JS, Java, or C# bootstrap should use ephemeral Docker (matches execution auto/docker vs local).
func shouldUseDockerBootstrap(runnerType, execution, lang string) bool {
	k := strings.ToLower(strings.TrimSpace(lang))
	if !isJSLang(lang) && k != "java" && k != "csharp" && k != "cs" {
		return false
	}
	ex := strings.ToLower(strings.TrimSpace(execution))
	if ex == "local" {
		return false
	}
	if ex == "docker" {
		return true
	}
	// auto (empty or "auto")
	return strings.ToLower(strings.TrimSpace(runnerType)) == "docker"
}

// resolveEphemeralDocker returns nil when bootstrap should run on the host.
// hostGitRoot is the repository checkout root (Docker volume source). workspaceAbs is the project folder (may equal hostGitRoot).
func resolveEphemeralDocker(rc *config.RunnerConfig, runnerType, execution, lang, hostGitRoot, workspaceAbs, runnerTimeout string, kind BootstrapDockerImage, extraDockerEnv []string) (*EphemeralDocker, error) {
	if !shouldUseDockerBootstrap(runnerType, execution, lang) {
		return nil, nil
	}
	if rc == nil {
		return nil, fmt.Errorf("test bootstrap: docker execution requires runner config")
	}
	return NewEphemeralDocker(rc, hostGitRoot, workspaceAbs, lang, runnerTimeout, kind, extraDockerEnv)
}

// enforceRequireDockerBootstrap fails when runner.require_docker_bootstrap is set but bootstrap would run on the host (no ephemeral Docker) for TS/JS, Java, or C#.
// Call only when an install/apply step will run (not when skipping because a framework is already detected).
func enforceRequireDockerBootstrap(rc *config.RunnerConfig, lang string, ed *EphemeralDocker, cfgPrefix string) error {
	if rc == nil || !rc.RequireDockerBootstrap {
		return nil
	}
	if !isJSLang(lang) && strings.ToLower(strings.TrimSpace(lang)) != "java" && !isCSharpLang(lang) {
		return nil
	}
	if ed != nil {
		return nil
	}
	prefix := strings.TrimSpace(cfgPrefix)
	if prefix == "" {
		prefix = "bootstrap"
	}
	return fmt.Errorf("%s: require_docker_bootstrap is true but install would run on the host for language %q; set runner.type to docker and/or %s.execution to docker", prefix, lang, prefix)
}

// NewEphemeralDocker builds settings for ephemeral bootstrap runs from runner config.
// hostGitRoot is the git root on disk (bind-mounted at /workspace). workspaceAbs is the toolchain cwd (mono subfolder or same as hostGitRoot).
func NewEphemeralDocker(rc *config.RunnerConfig, hostGitRoot, workspaceAbs, lang, runnerTimeout string, kind BootstrapDockerImage, extraDockerEnv []string) (*EphemeralDocker, error) {
	hostGitRoot = strings.TrimSpace(hostGitRoot)
	workspaceAbs = strings.TrimSpace(workspaceAbs)
	if hostGitRoot == "" {
		hostGitRoot = workspaceAbs
	}
	if workspaceAbs == "" {
		workspaceAbs = hostGitRoot
	}
	gitAbs, err := filepath.Abs(hostGitRoot)
	if err != nil {
		return nil, err
	}
	wsAbs, err := filepath.Abs(workspaceAbs)
	if err != nil {
		return nil, err
	}
	containerWD, err := containerDockerWorkdir(gitAbs, wsAbs)
	if err != nil {
		return nil, err
	}
	l := normalizeLangForProfile(lang)
	p, err := profile.ResolveToolchain(wsAbs, l, strings.TrimSpace(rc.EvalProfile), rc.ImageJavaMaven, rc.ImageJavaGradle, rc.ImageNode, rc.ImageDotNet)
	if err != nil {
		return nil, fmt.Errorf("bootstrap docker: %w", err)
	}
	img := p.Image
	switch kind {
	case BootstrapDockerPlaywrightJS:
		if isJSLang(lang) {
			if s := strings.TrimSpace(rc.ImagePlaywright); s != "" {
				img = s
			} else {
				img = DefaultPlaywrightDockerImage
			}
		}
	case BootstrapDockerPlaywrightJava:
		if strings.ToLower(strings.TrimSpace(lang)) == "java" {
			if s := strings.TrimSpace(rc.ImagePlaywrightJava); s != "" {
				img = s
			} else {
				img = DefaultPlaywrightJavaDockerImage
			}
		}
	case BootstrapDockerPlaywrightDotNet:
		if isCSharpLang(lang) {
			if s := strings.TrimSpace(rc.ImagePlaywrightDotnet); s != "" {
				img = s
			} else {
				img = runner.DefaultPlaywrightDotnetDockerImage
			}
		}
	}
	ipcHost := kind == BootstrapDockerPlaywrightJS || kind == BootstrapDockerPlaywrightJava || kind == BootstrapDockerPlaywrightDotNet
	net := strings.TrimSpace(rc.JobNetworkRestore)
	if net == "" {
		net = "bridge"
	}
	if rc.DockerDisableOfflineTest {
		net = strings.TrimSpace(rc.JobNetworkTest)
		if net == "" {
			net = "bridge"
		}
	}
	// Dependency installs need registry access.
	if net == "none" {
		net = "bridge"
	}
	d := installTimeout(runnerTimeout)
	bin := strings.TrimSpace(rc.DockerBinary)
	if bin == "" {
		bin = "docker"
	}
	var xenv []string
	if len(extraDockerEnv) > 0 {
		xenv = append([]string(nil), extraDockerEnv...)
	}
	if p.ID == profile.CSharpDotnet {
		xenv = append(xenv, "NuGetAudit=false")
	}
	return &EphemeralDocker{
		dockerBin:        bin,
		image:            img,
		hostAbs:          gitAbs,
		containerWorkdir: containerWD,
		network:          net,
		timeout:          d,
		memory:           strings.TrimSpace(rc.JobMemory),
		cpus:             rc.JobCPUs,
		pidsLimit:        rc.JobPidsLimit,
		mounts:           bootstrapCacheMounts(p.ID, rc),
		readonly:         rc.JobReadonlyRootfs,
		ipcHost:          ipcHost,
		extraDockerEnv:   xenv,
	}, nil
}

func bootstrapCacheMounts(id profile.ToolchainID, rc *config.RunnerConfig) []jobrunner.CacheMount {
	if rc == nil {
		return nil
	}
	var m []jobrunner.CacheMount
	add := func(host, target string) {
		host = strings.TrimSpace(host)
		if host == "" || target == "" {
			return
		}
		m = append(m, jobrunner.CacheMount{Source: host, Target: target})
	}
	switch id {
	case profile.JavaMaven, profile.JavaMaven11, profile.JavaMaven21:
		add(rc.CacheMavenHost, "/root/.m2")
	case profile.JavaGradle, profile.JavaGradle11, profile.JavaGradle21:
		add(rc.CacheGradleHost, "/root/.gradle")
	case profile.TypeScriptNPM:
		add(rc.CacheNpmHost, "/root/.npm")
	case profile.TypeScriptPNPM:
		add(rc.CacheNpmHost, "/root/.npm")
		add(rc.CachePnpmHost, "/root/.local/share/pnpm/store")
	case profile.TypeScriptYarn:
		add(rc.CacheNpmHost, "/root/.npm")
	case profile.CSharpDotnet:
		add(rc.CacheNuGetHost, "/root/.nuget/packages")
	}
	// Ecosystem-matched private-registry credential mounts. Materialising may fail (e.g. unwritable
	// TMPDIR) — we log-and-continue rather than fail bootstrap because a missing settings.xml or
	// .npmrc manifests later as a clear NU1301/401-style restore error with actionable remediation
	// already wired up in the evaluator diagnostics.
	if extras, err := rc.MaterialisePrivateRegistryMounts(); err == nil {
		for _, extra := range extras {
			if !bootstrapPrivateRegistryMountApplies(extra.ContainerPath, id) {
				continue
			}
			m = append(m, jobrunner.CacheMount{Source: extra.HostPath, Target: extra.ContainerPath, ReadOnly: extra.ReadOnly})
		}
	}
	return m
}

// bootstrapPrivateRegistryMountApplies mirrors privateRegistryMountAppliesToProfile in the runner
// package: gate mounts by target path so a .npmrc does not land on a Maven image and vice-versa.
// Duplicated (rather than exported) to keep the runner→testbootstrap dependency direction one-way.
func bootstrapPrivateRegistryMountApplies(target string, id profile.ToolchainID) bool {
	switch {
	case target == "/root/.m2/settings.xml":
		return id == profile.JavaMaven || id == profile.JavaMaven11 || id == profile.JavaMaven21
	case target == "/root/.npmrc":
		return id == profile.TypeScriptNPM || id == profile.TypeScriptPNPM || id == profile.TypeScriptYarn
	default:
		return false
	}
}
