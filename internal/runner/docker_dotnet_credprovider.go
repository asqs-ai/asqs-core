package runner

import "strings"

// nugetVSSEnvName is the env var the Microsoft Artifacts Credential Provider plugin reads
// to pick up per-feed credentials (PAT for Azure DevOps, username/password for generic
// HTTPS NuGet sources). ASQS injects it via `docker run -e` from AzureDevOpsNuGetDockerEnv.
const nugetVSSEnvName = "VSS_NUGET_EXTERNAL_FEED_ENDPOINTS"

// NuGetCredentialProviderDockerInstallShell returns a POSIX shell snippet that installs
// Microsoft's Artifacts Credential Provider into $HOME/.nuget/plugins/netcore inside an
// ephemeral container, so `dotnet restore` can consume VSS_NUGET_EXTERNAL_FEED_ENDPOINTS.
//
// Rationale: the stock `mcr.microsoft.com/dotnet/sdk:*` images do not ship the plugin.
// Without it, dotnet has no code path that reads the envelope and any private feed —
// Azure DevOps or otherwise — falls back to anonymous access and fails with
//
//	NU1301 Unable to load the service index for source …
//
// The snippet is idempotent (skips reinstall when the plugin dir already exists) and
// non-fatal: if the download fails (no network, GitHub outage, uncommon arch) it prints
// a warning to stderr and returns 0, so the subsequent `dotnet …` command proceeds and
// reproduces the same NU1301 as before — no regression.
//
// Self-contained (v2.0.0+) archives are used so the plugin runs independently of the
// SDK image's bundled .NET runtime major (sdk:10.0 does not bundle net8). A runtime-
// dependent `.Net8.` archive is kept as a fallback for architectures that lack a
// self-contained asset. Extracts `plugins/netcore` from the tarball directly into
// `$HOME/.nuget/` to match the layout documented in the upstream README.
func NuGetCredentialProviderDockerInstallShell() string {
	return `[ -d "$HOME/.nuget/plugins/netcore/CredentialProvider.Microsoft" ] ` +
		`|| { mkdir -p "$HOME/.nuget/plugins" ` +
		`&& _rid=linux-x64 ` +
		`&& case "$(uname -m)" in aarch64|arm64) _rid=linux-arm64 ;; esac ` +
		`&& { curl -fsSL -o /tmp/asqs-credprovider.tgz "https://github.com/microsoft/artifacts-credprovider/releases/latest/download/Microsoft.${_rid}.NuGet.CredentialProvider.tar.gz" 2>/dev/null ` +
		`|| curl -fsSL -o /tmp/asqs-credprovider.tgz "https://github.com/microsoft/artifacts-credprovider/releases/latest/download/Microsoft.Net8.NuGet.CredentialProvider.tar.gz"; } ` +
		`&& tar -xzf /tmp/asqs-credprovider.tgz -C "$HOME/.nuget/" plugins/netcore ` +
		`&& rm -f /tmp/asqs-credprovider.tgz; } ` +
		`|| echo "[asqs] warning: artifacts-credprovider install failed; NuGet private feed auth may not work" >&2`
}

// DockerEvalEnvHasNuGetCredentialEnvelope is true when env contains a
// VSS_NUGET_EXTERNAL_FEED_ENDPOINTS=… entry (i.e. ASQS is authenticating private feeds
// and therefore needs the credential provider plugin installed at run time).
func DockerEvalEnvHasNuGetCredentialEnvelope(env []string) bool {
	prefix := nugetVSSEnvName + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
