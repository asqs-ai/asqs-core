package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// Private-registry credentials on the local target (CP33; compile-only in the open core — no credential is ever materialised, see config/private_registry_compat.go).
//
// general.sandbox.registries.credentials and general.sandbox.registries.azure_devops_nuget_feed_endpoints were read only
// by the Docker eval path: the generated settings.xml and .npmrc became container mounts and the
// NuGet envelope became a `-e`. Under runner.type: local all of it was inert, and the only signal
// was a startup warning. For a deployment behind a private Artifactory or Nexus that was the
// difference between a working run and `mvn` failing on a 401 with no explanation.
//
// The files are the same ones; only the delivery differs, and it differs because a host has no
// mount table:
//
//	maven  container: mounted at /root/.m2/settings.xml   local: `mvn -s <hostpath>`
//	npm    container: mounted at /root/.npmrc             local: npm_config_userconfig=<hostpath>
//	nuget  container: VSS_NUGET_EXTERNAL_FEED_ENDPOINTS   local: the same variable
//
// The files keep their existing lifetime: a per-PID 0700 temp directory with 0600 files,
// deliberately NOT deleted on exit so an operator can inspect exactly what the sandbox saw when
// auth fails.

// CredentialFile is a generated credential file and the ecosystem that consumes it.
type CredentialFile struct {
	Ecosystem config.PrivateRegistryEcosystem
	HostPath  string
}

func credentialFilesFromConfig(mounts []config.PrivateRegistryMount) []CredentialFile {
	out := make([]CredentialFile, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, CredentialFile{Ecosystem: m.Ecosystem, HostPath: m.HostPath})
	}
	return out
}

func (s *Sandbox) credentialFor(eco config.PrivateRegistryEcosystem) string {
	for _, c := range s.PrivateRegistryCredentials {
		if c.Ecosystem == eco && strings.TrimSpace(c.HostPath) != "" {
			return c.HostPath
		}
	}
	return ""
}

// applyLocalMavenSettings inserts `-s <hostpath>` into a Maven invocation.
//
// The container gets the file mounted at Maven's default location and needs no flag; a host has no
// such mount, so the path must be named. Inserted immediately after the binary, before any goal,
// because Maven only accepts options ahead of the lifecycle phase.
func applyLocalMavenSettings(argv []string, settingsPath string) []string {
	if len(argv) == 0 || strings.TrimSpace(settingsPath) == "" {
		return argv
	}
	for _, a := range argv {
		if a == "-s" || a == "--settings" {
			return argv // an explicit settings flag from the operator wins
		}
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "-s", settingsPath)
	return append(out, argv[1:]...)
}

// localCredentialEnv returns the environment entries that deliver credentials to a host process.
func (s *Sandbox) localCredentialEnv(id profile.ToolchainID) []string {
	var env []string
	switch id {
	case profile.TypeScriptNPM, profile.TypeScriptPNPM, profile.TypeScriptYarn:
		// npm, pnpm and yarn all honour npm_config_userconfig; it is the host equivalent of
		// mounting the file at ~/.npmrc.
		if p := s.credentialFor(config.EcosystemNPM); p != "" {
			env = append(env, "npm_config_userconfig="+p)
		}
	case profile.CSharpDotnet:
		// The NuGet envelope has no file form — it is only ever an environment variable, on either
		// target (see config.AzureDevOpsNuGetDockerEnv).
		env = append(env, s.DockerEvalExtraEnv...)
	}
	return env
}

// nuGetCredentialProviderInstalled reports whether the Artifacts credential provider plugin is
// present in the current user's NuGet plugin directory.
func nuGetCredentialProviderInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	for _, rel := range []string{
		filepath.Join(".nuget", "plugins", "netcore", "CredentialProvider.Microsoft"),
		filepath.Join(".nuget", "plugins", "netfx", "CredentialProvider.Microsoft"),
	} {
		if st, serr := os.Stat(filepath.Join(home, rel)); serr == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// warnLocalNuGetCredentialProviderMissing reports the one thing the local target cannot supply for
// itself.
//
// Docker installs the plugin INTO the container; ASQS must not install software into an operator's
// home directory, so the host is told instead. A warning rather than a hard failure: the envelope
// being inert does not prove the run will fail — an operator may authenticate the same feed through
// a host nuget.config — and turning that into a blocked run would be a regression. What it does
// guarantee is that NU1301's real cause is stated up front rather than inferred from a restore log.
func (s *Sandbox) warnLocalNuGetCredentialProviderMissing() {
	if len(s.DockerEvalExtraEnv) == 0 || nuGetCredentialProviderInstalled() {
		return
	}
	s.runState().nugetPluginWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"config: a NuGet credential envelope is configured (general.sandbox.registries.azure_devops_nuget_feed_endpoints "+
				"or a general.sandbox.registries.credentials entry of type nuget) but the Azure Artifacts credential "+
				"provider is not installed for this user, so `dotnet restore` will ignore it and fail against "+
				"a private feed with NU1301.\n"+
				"  Install it with:\n"+
				"    sh -c \"$(curl -fsSL https://aka.ms/install-artifacts-credprovider.sh)\"\n"+
				"  Or set general.sandbox.type to docker, which installs the plugin into the container itself.\n")
	})
}
