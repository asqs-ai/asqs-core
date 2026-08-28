package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

func TestNuGetCredentialProviderDockerInstallShell_shape(t *testing.T) {
	s := NuGetCredentialProviderDockerInstallShell()
	if s == "" {
		t.Fatal("expected non-empty install snippet")
	}
	for _, want := range []string{
		`$HOME/.nuget/plugins/netcore/CredentialProvider.Microsoft`, // idempotence guard
		`$HOME/.nuget/plugins`, // install target
		`aarch64|arm64`,        // arch detection
		`linux-x64`,            // default RID
		`linux-arm64`,          // ARM RID
		`Microsoft.${_rid}.NuGet.CredentialProvider.tar.gz`, // self-contained asset
		`Microsoft.Net8.NuGet.CredentialProvider.tar.gz`,    // runtime-dependent fallback
		`tar -xzf`,
		`plugins/netcore`,
		`[asqs] warning`, // non-fatal failure path
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install snippet missing %q; got:\n%s", want, s)
		}
	}
}

func TestDockerEvalEnvHasNuGetCredentialEnvelope(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{"empty", nil, false},
		{"unrelated", []string{"CI=true", "FOO=bar"}, false},
		{"prefix only, no value", []string{"VSS_NUGET_EXTERNAL_FEED_ENDPOINTS="}, true},
		{"present", []string{"CI=true", `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[]}`}, true},
		{"substring not enough", []string{"MY_VSS_NUGET_EXTERNAL_FEED_ENDPOINTS=foo"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DockerEvalEnvHasNuGetCredentialEnvelope(tc.env); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Ensures `dotnet` commands invoked via docker eval are transparently wrapped with the credential
// provider install whenever ASQS is injecting the VSS_NUGET_EXTERNAL_FEED_ENDPOINTS envelope.
//
// The prepend moved out of patchDotnetEvalArgv into applyDotnetContainerProvisioning so it runs
// LAST, after the MSBuild-property insertions — those are anchored at the start of the line and a
// prepended snippet silently stopped them matching. See
// TestDotnetProvisioningPrepend_doesNotStripTestProtections.
func TestDotnetContainerProvisioning_prependsCredProviderWhenVSSEnvSet(t *testing.T) {
	sb := &Sandbox{
		DockerEvalExtraEnv: []string{`VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[{"endpoint":"https://pkgs.dev.azure.com/o/_packaging/F/nuget/v3/index.json","username":"x","password":"y"}]}`},
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet, Image: "mcr.microsoft.com/dotnet/sdk:10.0"}
	argv := []string{"dotnet", "build", "App.sln", "-c", "Release"}

	out := sb.applyDotnetContainerProvisioning(p, argv, t.TempDir(), t.TempDir())
	if len(out) != 3 || out[0] != "sh" || out[1] != "-c" {
		t.Fatalf("expected sh -c wrapper, got %v", out)
	}
	if !strings.Contains(out[2], "CredentialProvider.Microsoft") {
		t.Errorf("expected credential provider install in wrapped script, got:\n%s", out[2])
	}
	if !strings.Contains(out[2], "dotnet") || !strings.Contains(out[2], "build") {
		t.Errorf("expected original dotnet build to remain in script, got:\n%s", out[2])
	}
}

// Verifies the credential provider install is NOT prepended when VSS_NUGET_EXTERNAL_FEED_ENDPOINTS
// is absent, so repos without private feeds don't pay the download tax on every container run.
func TestDotnetContainerProvisioning_noOpWithoutVSSEnv(t *testing.T) {
	sb := &Sandbox{} // no DockerEvalExtraEnv
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet, Image: "mcr.microsoft.com/dotnet/sdk:10.0"}
	argv := []string{"dotnet", "build", "App.sln", "-c", "Release"}

	out := sb.applyDotnetContainerProvisioning(p, argv, t.TempDir(), t.TempDir())
	joined := strings.Join(out, " ")
	if strings.Contains(joined, "CredentialProvider.Microsoft") {
		t.Errorf("did not expect credential provider install when VSS env is absent, got:\n%s", joined)
	}
}
