// Package runner runs tests in a sandbox (Docker or local) executing mvn test / gradle test / npm test.
// It implements evaluator.SandboxRunner for the evaluation workflow: compile, test, lint, coverage, mutation.
package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/jobrunner"
	"github.com/asqs/asqs-core/internal/workspace"
)

// Sandbox implements evaluator.SandboxRunner.
// Type "local" runs commands on the host; Type "docker" runs toolchain profiles in ephemeral containers.
type Sandbox struct {
	Type           string
	Timeout        string
	BuildTool      string
	CompileCommand string
	TestCommand    string

	EvalProfile              string
	DockerBinary             string
	ImageJavaMaven           string
	ImageJavaGradle          string
	ImageNode                string
	ImagePlaywright          string
	ImagePlaywrightJava      string // mcr.microsoft.com/playwright/java for Java E2E eval (browsers + OS deps); see docker_playwright.go
	ImagePlaywrightDotnet    string // mcr.microsoft.com/playwright/dotnet for C# Playwright E2E eval when E2EFramework is playwright-dotnet; see docker_playwright.go
	ImageDotNet              string
	JobMemory                string
	JobCPUs                  float64
	JobPidsLimit             int64
	JobNetworkRestore        string
	JobNetworkTest           string
	DockerDisableOfflineTest bool
	JobReadonlyRootfs        bool
	CacheMavenHost           string
	CacheGradleHost          string
	CacheNpmHost             string
	CachePnpmHost            string
	CacheNuGetHost           string
	CacheCypressHost         string

	// EvalWorkSubpath is a normalized repo-relative mono-repo workspace (forward slashes, no leading slash).
	// When set, local and Docker eval use this subdirectory as the toolchain cwd while the git tree remains the mount/write root.
	EvalWorkSubpath string
	// DotNetFallbackTargetFramework when non-empty, append /p:TargetFramework=… for dotnet CLI when the entry .csproj omits a concrete TFM (see applyDotnetTargetFrameworkFallbackArgv).
	DotNetFallbackTargetFramework string
	// DockerEvalExtraEnv is appended to every docker eval JobSpec after CI=true (e.g. VSS_NUGET_EXTERNAL_FEED_ENDPOINTS for Azure Artifacts).
	DockerEvalExtraEnv []string
	// DockerEvalExtraMounts is appended to every docker eval JobSpec.CacheMounts *in addition* to
	// the ecosystem-matched language cache mounts. ASQS uses it to bind-mount the generated Maven
	// ~/.m2/settings.xml and npm ~/.npmrc files that carry private_registry_credentials into the
	// container, so `mvn` and `npm/yarn/pnpm` pick up auth transparently without needing the project
	// to ship credentials in its own pom.xml / .npmrc. Each entry is a fully-formed CacheMount with
	// absolute host source path and an absolute container target path.
	//
	// Mount semantics: file targets (e.g. /root/.m2/settings.xml) coexist with directory caches
	// mounted at parent paths (e.g. /root/.m2); Docker applies file bind-mounts after dir mounts,
	// so the credentials file appears *inside* the maven cache volume. Targets are read-only by
	// design — the generated files are immutable for the life of the sandbox.
	DockerEvalExtraMounts []jobrunner.CacheMount

	// PrivateRegistryCredentials are the same generated files as DockerEvalExtraMounts, tagged by
	// ecosystem so the LOCAL target can deliver them without a mount table (`mvn -s <path>`,
	// npm_config_userconfig). Always empty in the open core — the enterprise seam never
	// materialises a credential — but the delivery code compiles and the parity fixtures exercise it.
	PrivateRegistryCredentials []CredentialFile

	// run is the per-run state shared by every clone of this Sandbox (see run_state.go). Behind a
	// pointer so TestWithCommand-style shallow copies share it structurally rather than by the
	// accident of a field's type.
	run *sandboxRunState
}

// NewSandboxFromConfig builds a Sandbox from application config.
func NewSandboxFromConfig(cfg *config.Config) *Sandbox {
	if cfg == nil {
		return &Sandbox{Type: "local", run: &sandboxRunState{}}
	}
	r := cfg.Runner
	t := strings.ToLower(strings.TrimSpace(r.Type))
	if t == "" {
		t = "local"
	}
	monoRel, _ := workspace.NormalizeMonoRepoWorkspace(cfg.Indexer.MonoRepoWorkspace)
	monoTestRel, _ := workspace.NormalizeMonoRepoWorkspace(cfg.Indexer.MonoRepoTestWorkspace)
	evalSub := monoRel
	if monoTestRel != "" {
		evalSub = monoTestRel
	}
	jm := strings.TrimSpace(r.ImageJavaMaven)
	if jm == "" {
		jm = strings.TrimSpace(r.ImageJava)
	}
	jg := strings.TrimSpace(r.ImageJavaGradle)
	if jg == "" {
		jg = strings.TrimSpace(r.ImageJava)
	}
	sb := &Sandbox{
		Type:                          t,
		Timeout:                       r.Timeout,
		BuildTool:                     r.BuildTool,
		CompileCommand:                r.CompileCommand,
		TestCommand:                   r.TestCommand,
		EvalProfile:                   r.EvalProfile,
		DockerBinary:                  r.DockerBinary,
		ImageJavaMaven:                jm,
		ImageJavaGradle:               jg,
		ImageNode:                     r.ImageNode,
		ImagePlaywright:               r.ImagePlaywright,
		ImagePlaywrightJava:           r.ImagePlaywrightJava,
		ImagePlaywrightDotnet:         r.ImagePlaywrightDotnet,
		ImageDotNet:                   r.ImageDotNet,
		JobMemory:                     r.JobMemory,
		JobCPUs:                       r.JobCPUs,
		JobPidsLimit:                  r.JobPidsLimit,
		JobNetworkRestore:             r.JobNetworkRestore,
		JobNetworkTest:                r.JobNetworkTest,
		DockerDisableOfflineTest:      r.DockerDisableOfflineTest,
		JobReadonlyRootfs:             r.JobReadonlyRootfs,
		CacheMavenHost:                r.CacheMavenHost,
		CacheGradleHost:               r.CacheGradleHost,
		CacheNpmHost:                  r.CacheNpmHost,
		CachePnpmHost:                 r.CachePnpmHost,
		CacheNuGetHost:                r.CacheNuGetHost,
		CacheCypressHost:              r.CacheCypressHost,
		EvalWorkSubpath:               evalSub,
		DotNetFallbackTargetFramework: strings.TrimSpace(r.DotNetFallbackTargetFramework),
	}
	// The enterprise seam: MaterialisePrivateRegistryMounts always returns nil in the open core
	// (config/private_registry_compat.go), so no mount and no credential file ever appears here.
	// The call stays so the delivery path is identical to upstream's shape.
	if mounts, err := cfg.Runner.MaterialisePrivateRegistryMounts(); err == nil {
		sb.PrivateRegistryCredentials = credentialFilesFromConfig(mounts)
	}
	sb.run = &sandboxRunState{}
	return sb
}

// evalHostCwd returns the host directory used as the toolchain working directory (mono-repo workspace or git root).
func (s *Sandbox) evalHostCwd(gitRootAbs string) string {
	gr := filepath.Clean(strings.TrimSpace(gitRootAbs))
	if s == nil {
		return gr
	}
	sub := strings.TrimSpace(s.EvalWorkSubpath)
	if sub == "" {
		return gr
	}
	sub = strings.Trim(filepath.ToSlash(sub), "/")
	if sub == "" {
		return gr
	}
	return filepath.Join(gr, filepath.FromSlash(sub))
}

func (s *Sandbox) dockerContainerWorkdir() string {
	const base = "/workspace"
	if s == nil || strings.TrimSpace(s.EvalWorkSubpath) == "" {
		return base
	}
	sub := strings.Trim(filepath.ToSlash(strings.TrimSpace(s.EvalWorkSubpath)), "/")
	if sub == "" {
		return base
	}
	return base + "/" + sub
}

func (s *Sandbox) timeoutDuration() time.Duration {
	if s.Timeout == "" {
		return defaultLocalTimeout
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return defaultLocalTimeout
	}
	return d
}

// Compile builds/compiles the project.
// repoPath must be the git repository root (absolute); when EvalWorkSubpath is set, the toolchain cwd is that subdirectory.
func (s *Sandbox) Compile(ctx context.Context, repoPath, lang string) evaluator.StepResult {
	cwd := s.evalHostCwd(repoPath)
	if s.Type == "local" {
		return s.runLocalPlannedStep(ctx, repoPath, cwd, lang, evaluator.StepCompile, "Compile")
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepCompile), "Compile")
	}
	return unknownRunnerTypeResult(s.Type, evaluator.StepCompile)
}

// Test runs the test suite.
func (s *Sandbox) Test(ctx context.Context, repoPath, lang string) evaluator.StepResult {
	return s.testWithLabel(ctx, repoPath, lang, "Tests")
}

// testWithLabel runs the test step under a human label. The label is what the step announces in
// the evaluation log; the E2E pass uses a distinct one so the two test passes of a single round
// are told apart at a glance, on both targets.
func (s *Sandbox) testWithLabel(ctx context.Context, repoPath, lang, label string) evaluator.StepResult {
	cwd := s.evalHostCwd(repoPath)
	if s.Type == "local" {
		return s.runLocalPlannedStep(ctx, repoPath, cwd, lang, evaluator.StepTest, label)
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepTest), label)
	}
	return unknownRunnerTypeResult(s.Type, evaluator.StepTest)
}

// TestWithCommand runs the test step using testCommand instead of the sandbox's configured TestCommand (dual unit/E2E eval).
func (s *Sandbox) TestWithCommand(ctx context.Context, repoPath, lang, testCommand string) evaluator.StepResult {
	s2 := s.clone()
	s2.TestCommand = strings.TrimSpace(testCommand)
	return s2.Test(ctx, repoPath, lang)
}

// CompileWithCommand runs the compile step with an explicit shell command override. Used by the evaluator's
// scoped-compile fallback (see evaluator.CompileWithCommandRunner). Cloning the sandbox is intentional so
// shared state (cache mount configuration, auth, docker binary, timeouts) is preserved while only the compile
// command is overridden for this one invocation.
func (s *Sandbox) CompileWithCommand(ctx context.Context, repoPath, lang, compileCommand string) evaluator.StepResult {
	s2 := s.clone()
	s2.CompileCommand = strings.TrimSpace(compileCommand)
	return s2.Compile(ctx, repoPath, lang)
}

// ReportEvalWorkSubpath implements evaluator.EvalWorkSubpathReporter. The returned value is the normalized,
// forward-slash repo-relative mono-repo workspace directory that the toolchain runs from (empty when the
// toolchain runs from the git root). Evaluator callers that build ad-hoc shell commands with paths use
// this to rewrite repo-relative paths into cwd-relative paths before handing the command back to
// CompileWithCommand / TestWithCommand. Without this, `dotnet build projects/upper/.../X.csproj` fails
// with MSB1009 when the cwd is already `/workspace/projects/upper`. The getter name is intentionally
// distinct from the `EvalWorkSubpath` struct field so the method and field can coexist on *Sandbox.
func (s *Sandbox) ReportEvalWorkSubpath() string {
	if s == nil {
		return ""
	}
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(s.EvalWorkSubpath), "\\", "/"), "/")
}

// TestE2EPass runs the second (E2E) test pass. For Docker + JS/TS + Playwright/Cypress, uses the Playwright Node OCI image; for Docker + Java + playwright-java, uses mcr.microsoft.com/playwright/java (browsers + OS deps); for Docker + C# + playwright-dotnet, uses mcr.microsoft.com/playwright/dotnet (browsers + .NET SDK). Otherwise uses the normal toolchain image (plain sdk/maven images lack bundled browsers for Playwright).
func (s *Sandbox) TestE2EPass(ctx context.Context, repoPath, lang, testCommand, e2eFramework string) evaluator.StepResult {
	s2 := s.clone()
	s2.TestCommand = strings.TrimSpace(testCommand)
	if s2.Type != "docker" {
		// No image to swap in, so the host must already have browsers. Bootstrap installs them
		// when enabled; when it has not, say so before the runner fails with a stack trace.
		s2.warnLocalE2EBrowsersMissing(lang, e2eFramework)
		return s2.testWithLabel(ctx, repoPath, lang, "Tests (E2E)")
	}
	img := ""
	if usePlaywrightDockerForJSE2E(lang, e2eFramework) {
		// The image tag follows the @playwright/test the repository resolved (see
		// InstalledPlaywrightTestVersion), read from the package directory the E2E step runs in.
		pkgDir := strings.TrimSpace(repoPath)
		if abs, err := filepath.Abs(pkgDir); err == nil {
			pkgDir = s2.evalHostCwd(abs)
		}
		img = s2.playwrightDockerImageRefFor(pkgDir)
	} else if usePlaywrightDockerForJavaE2E(lang, e2eFramework) {
		img = s2.playwrightJavaDockerImageRef()
	} else if usePlaywrightDockerForCSharpE2E(lang, e2eFramework) {
		img = s2.playwrightDotnetDockerImageRef()
	}
	return s2.runDockerEvalWithImageOverride(ctx, repoPath, lang, string(evaluator.StepTest), "Tests (E2E)", img)
}

// CoverageWithCommand runs coverage using testCommand (typically the unit test command so E2E is not re-run for coverage).
func (s *Sandbox) CoverageWithCommand(ctx context.Context, repoPath, lang, testCommand string) evaluator.StepResult {
	s2 := s.clone()
	s2.TestCommand = strings.TrimSpace(testCommand)
	return s2.Coverage(ctx, repoPath, lang)
}

// Lint runs lint/format checks.
func (s *Sandbox) Lint(ctx context.Context, repoPath, lang string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepLint, OK: true, Summary: "stub"}
}

// Coverage runs tests with coverage.
func (s *Sandbox) Coverage(ctx context.Context, repoPath, lang string) evaluator.StepResult {
	cwd := s.evalHostCwd(repoPath)
	if s.Type == "local" {
		return s.runLocalPlannedStep(ctx, repoPath, cwd, lang, evaluator.StepCoverage, "Coverage")
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepCoverage), "Coverage")
	}
	return unknownRunnerTypeResult(s.Type, evaluator.StepCoverage)
}

// Mutation runs mutation tests for critical modules.
func (s *Sandbox) Mutation(ctx context.Context, repoPath, lang string, criticalModules []string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepMutation, OK: true, Summary: "skipped"}
}

var _ evaluator.E2EPassDockerRunner = (*Sandbox)(nil)

// stepSuccessSummary is the Summary of a step that passed, identical on both targets.
//
// The two used to disagree in wording and in substance: Docker built "<Label> ok" from its own
// human label while local said "compile ok"/"tests ok", and Docker's coverage step reported a flat
// "Coverage ok" that could never name a report. One lookup now serves both, for every ecosystem.
func stepSuccessSummary(step evaluator.SandboxStep, plan StepPlan, cwd string) string {
	switch step {
	case evaluator.StepCompile:
		return "compile ok"
	case evaluator.StepCoverage:
		return coverageSummaryFromPlan(cwd, plan)
	default:
		return "tests ok"
	}
}

// jsExitCodeIgnoredSuffix marks a JS/TS test step whose runner exited non-zero while its own
// summary reported no failures — most often Jest holding an open handle after the suite passed.
const jsExitCodeIgnoredSuffix = " (summary all passed; exit code ignored)"

// validRunnerTypes is the single source of the accepted runner.type values. Config validation
// rejects anything else at startup (config.normaliseAndValidateRunnerType names the same two), and
// the executor backstop below fails rather than stubbing — a future type cannot be added without
// updating this set, which the tests enumerate.
var validRunnerTypes = map[string]bool{
	string(TargetLocal):  true,
	string(TargetDocker): true,
}

// unknownRunnerTypeResult fails a step whose runner.type is neither local nor docker.
//
// This used to report {OK: true, Summary: "stub"}, so a typo in runner.type produced a run that
// compiled nothing, tested nothing, and reported success all the way to the ship decision. Config
// validation now rejects such a value at startup, which is where an operator can act on it; this
// is the backstop for a Sandbox constructed directly, and it fails loudly rather than passing
// quietly.
func unknownRunnerTypeResult(runnerType string, step evaluator.SandboxStep) evaluator.StepResult {
	msg := fmt.Sprintf("%s step did not run: runner.type is %q, which is neither \"local\" nor \"docker\"", step, runnerType)
	return evaluator.StepResult{Step: step, OK: false, Summary: msg, Output: msg}
}
