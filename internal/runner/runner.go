// Package runner runs tests in a sandbox (Docker or local) executing mvn test / gradle test / npm test.
// It implements evaluator.SandboxRunner for the evaluation workflow: compile, test, lint, coverage, mutation.
package runner

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
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

	dockerEvalEnvOnce *sync.Once // log full docker eval environment once per Sandbox
	localEvalEnvOnce  *sync.Once // log local eval once per Sandbox
}

// NewSandboxFromConfig builds a Sandbox from application config.
func NewSandboxFromConfig(cfg *config.Config) *Sandbox {
	if cfg == nil {
		return &Sandbox{Type: "local"}
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
	// (asqs-core: Azure DevOps NuGet env + private-registry credential mounts are an enterprise
	// feature and are intentionally omitted.)
	sb.dockerEvalEnvOnce = &sync.Once{}
	sb.localEvalEnvOnce = &sync.Once{}
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
		s.logLocalEvalEnvOnce(repoPath)
		return runLocalCompile(ctx, cwd, lang, s.timeoutDuration(), s.BuildTool, s.CompileCommand, s.TestCommand, s.DotNetFallbackTargetFramework)
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepCompile), "Compile")
	}
	return evaluator.StepResult{Step: evaluator.StepCompile, OK: true, Summary: "stub"}
}

// Test runs the test suite.
func (s *Sandbox) Test(ctx context.Context, repoPath, lang string) evaluator.StepResult {
	cwd := s.evalHostCwd(repoPath)
	if s.Type == "local" {
		return runLocalTest(ctx, cwd, lang, s.timeoutDuration(), s.BuildTool, s.CompileCommand, s.TestCommand, s.DotNetFallbackTargetFramework)
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepTest), "Tests")
	}
	return evaluator.StepResult{Step: evaluator.StepTest, OK: true, Summary: "stub"}
}

// TestWithCommand runs the test step using testCommand instead of the sandbox's configured TestCommand (dual unit/E2E eval).
func (s *Sandbox) TestWithCommand(ctx context.Context, repoPath, lang, testCommand string) evaluator.StepResult {
	s2 := *s
	s2.TestCommand = strings.TrimSpace(testCommand)
	return s2.Test(ctx, repoPath, lang)
}

// CompileWithCommand runs the compile step with an explicit shell command override. Used by the evaluator's
// scoped-compile fallback (see evaluator.CompileWithCommandRunner). Cloning the sandbox is intentional so
// shared state (cache mount configuration, auth, docker binary, timeouts) is preserved while only the compile
// command is overridden for this one invocation.
func (s *Sandbox) CompileWithCommand(ctx context.Context, repoPath, lang, compileCommand string) evaluator.StepResult {
	s2 := *s
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
	s2 := *s
	s2.TestCommand = strings.TrimSpace(testCommand)
	if s2.Type != "docker" {
		return s2.Test(ctx, repoPath, lang)
	}
	img := ""
	if usePlaywrightDockerForJSE2E(lang, e2eFramework) {
		img = s2.playwrightDockerImageRef()
	} else if usePlaywrightDockerForJavaE2E(lang, e2eFramework) {
		img = s2.playwrightJavaDockerImageRef()
	} else if usePlaywrightDockerForCSharpE2E(lang, e2eFramework) {
		img = s2.playwrightDotnetDockerImageRef()
	}
	return s2.runDockerEvalWithImageOverride(ctx, repoPath, lang, string(evaluator.StepTest), "Tests (E2E)", img)
}

// CoverageWithCommand runs coverage using testCommand (typically the unit test command so E2E is not re-run for coverage).
func (s *Sandbox) CoverageWithCommand(ctx context.Context, repoPath, lang, testCommand string) evaluator.StepResult {
	s2 := *s
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
		return runLocalCoverage(ctx, cwd, lang, s.timeoutDuration(), s.BuildTool, s.CompileCommand, s.TestCommand, s.DotNetFallbackTargetFramework)
	}
	if s.Type == "docker" {
		return s.runDockerEval(ctx, repoPath, lang, string(evaluator.StepCoverage), "Coverage")
	}
	return evaluator.StepResult{Step: evaluator.StepCoverage, OK: true, Summary: "stub"}
}

// Mutation runs mutation tests for critical modules.
func (s *Sandbox) Mutation(ctx context.Context, repoPath, lang string, criticalModules []string) evaluator.StepResult {
	return evaluator.StepResult{Step: evaluator.StepMutation, OK: true, Summary: "skipped"}
}

var _ evaluator.E2EPassDockerRunner = (*Sandbox)(nil)
