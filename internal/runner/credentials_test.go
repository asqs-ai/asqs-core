package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

func credSandbox(target string, files ...CredentialFile) *Sandbox {
	return &Sandbox{Type: target, Timeout: "30s", PrivateRegistryCredentials: files}
}

// The container gets settings.xml mounted at Maven's default path; a host has no mount table, so
// the path must be named. Before U6 the local runner silently used no credentials at all and failed
// on a 401 with no explanation.
func TestLocalCredentials_mavenSettingsReachTheArgv(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	repo := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	sb := credSandbox("local", CredentialFile{Ecosystem: config.EcosystemMaven, HostPath: "/tmp/creds/settings.xml"})

	plan, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range planSteps {
		argv := plan.ArgvFor(step)
		if len(argv) < 3 || argv[0] != "mvn" || argv[1] != "-s" || argv[2] != "/tmp/creds/settings.xml" {
			t.Errorf("%s: argv %v should start with `mvn -s <path>`", step, argv)
		}
	}
}

// Maven only accepts options before the lifecycle phase, so the flag must land immediately after
// the binary — appending it would produce `mvn test -s <path>`, which Maven rejects.
func TestApplyLocalMavenSettings_insertsBeforeTheGoal(t *testing.T) {
	got := applyLocalMavenSettings([]string{"mvn", "-q", "-B", "test"}, "/c/s.xml")
	want := "mvn -s /c/s.xml -q -B test"
	if strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", strings.Join(got, " "), want)
	}
}

// An operator who already passes -s wins; ASQS must not append a second settings file.
func TestApplyLocalMavenSettings_respectsAnExplicitFlag(t *testing.T) {
	in := []string{"mvn", "-s", "/my/own.xml", "test"}
	if got := applyLocalMavenSettings(in, "/generated.xml"); strings.Join(got, " ") != strings.Join(in, " ") {
		t.Errorf("got %v, want the argv unchanged", got)
	}
	if strings.Contains(strings.Join(applyLocalMavenSettings(in, "/generated.xml"), " "), "/generated.xml") {
		t.Error("the generated settings file must not override an explicit one")
	}
}

// npm, pnpm and yarn all honour npm_config_userconfig; it is the host equivalent of mounting the
// file at ~/.npmrc.
func TestLocalCredentials_npmrcReachesTheEnvironment(t *testing.T) {
	repo := writeRepoTree(t, map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, nil)
	sb := credSandbox("local", CredentialFile{Ecosystem: config.EcosystemNPM, HostPath: "/tmp/creds/npmrc"})

	plan, err := sb.buildStepPlan(repo, "typescript", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range planSteps {
		if !containsEnv(plan.EnvFor(step), "npm_config_userconfig=/tmp/creds/npmrc") {
			t.Errorf("%s: env %v missing npm_config_userconfig", step, plan.EnvFor(step))
		}
	}
}

// The NuGet envelope is the one credential with no file form on either target.
func TestLocalCredentials_nugetEnvelopeReachesTheLocalEnvironment(t *testing.T) {
	stubToolsOnPATH(t, "dotnet")
	repo := writeRepoTree(t, map[string]string{
		"App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
	}, nil)
	sb := &Sandbox{Type: "local", Timeout: "30s",
		DockerEvalExtraEnv: []string{`VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[]}`}}

	plan, err := sb.buildStepPlan(repo, "csharp", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range planSteps {
		if !containsEnv(plan.EnvFor(step), `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[]}`) {
			t.Errorf("%s: env %v missing the NuGet envelope", step, plan.EnvFor(step))
		}
	}
}

// Credentials must not leak into an ecosystem that cannot use them: a Java build has no business
// receiving an npm registry token, and the Docker side has always gated its mounts the same way.
func TestLocalCredentials_areEcosystemGated(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	repo := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	sb := credSandbox("local", CredentialFile{Ecosystem: config.EcosystemNPM, HostPath: "/tmp/creds/npmrc"})

	plan, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range planSteps {
		joined := strings.Join(plan.ArgvFor(step), " ") + " " + strings.Join(plan.EnvFor(step), " ")
		if strings.Contains(joined, "npmrc") {
			t.Errorf("%s: an npm credential reached a Maven build: %s", step, joined)
		}
	}
	if got := (&Sandbox{}).localCredentialEnv(profile.JavaMaven); len(got) != 0 {
		t.Errorf("java should take no credential env, got %v", got)
	}
}

// Docker keeps mounting the files; the local wiring must not change that.
func TestDockerCredentials_stillUseMountsNotFlags(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	repo := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	sb := credSandbox("docker", CredentialFile{Ecosystem: config.EcosystemMaven, HostPath: "/tmp/creds/settings.xml"})

	plan, err := sb.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.ArgvFor(evaluator.StepTest), " "); strings.Contains(got, "-s ") {
		t.Errorf("docker argv %q should rely on the mount, not a -s flag", got)
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// The one thing the local target cannot supply for itself: Docker installs the Artifacts
// credential provider INTO the container, and ASQS must not install software into an operator's
// home directory. A warning, not a failure — an inert envelope does not prove the run will fail,
// since the same feed may be authenticated through a host nuget.config, and blocking would be a
// regression. What it does guarantee is that NU1301's real cause is stated before the restore.
func TestWarnLocalNuGetCredentialProviderMissing(t *testing.T) {
	sb := &Sandbox{Type: "local", DockerEvalExtraEnv: []string{`VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={}`}}
	t.Setenv("HOME", t.TempDir()) // no plugin installed

	out := captureStderr(t, func() { sb.warnLocalNuGetCredentialProviderMissing() })

	for _, want := range []string{"credential provider is not installed", "NU1301", "install-artifacts-credprovider", "general.sandbox.type to docker"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning %q missing %q", out, want)
		}
	}
	// Once per run, not once per step.
	again := captureStderr(t, func() { sb.warnLocalNuGetCredentialProviderMissing() })
	if strings.TrimSpace(again) != "" {
		t.Errorf("warning repeated: %q", again)
	}
}

func TestWarnLocalNuGetCredentialProviderMissing_silentWhenInstalledOrUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No envelope configured: nothing to say.
	quiet := captureStderr(t, func() { (&Sandbox{Type: "local"}).warnLocalNuGetCredentialProviderMissing() })
	if strings.TrimSpace(quiet) != "" {
		t.Errorf("no envelope configured should warn nothing, got %q", quiet)
	}

	// Plugin present: nothing to say either.
	if err := os.MkdirAll(filepath.Join(home, ".nuget", "plugins", "netcore", "CredentialProvider.Microsoft"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Type: "local", DockerEvalExtraEnv: []string{`VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={}`}}
	got := captureStderr(t, func() { sb.warnLocalNuGetCredentialProviderMissing() })
	if strings.TrimSpace(got) != "" {
		t.Errorf("plugin installed should warn nothing, got %q", got)
	}
}
