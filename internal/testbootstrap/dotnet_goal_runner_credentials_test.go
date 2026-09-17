package testbootstrap

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every dotnet command that reaches a bootstrap container needs the Artifacts credential provider
// when an envelope is configured — the plugin is absent from the stock SDK image and the envelope
// is inert without it, so `dotnet restore` fails NU1301 against any private feed.
//
// The prepend used to live in RunArgvWithShellPrefix only, and dotnetGoalRunner.build went through
// RunArgv: the build of the generated test project triggers an implicit restore, so the FIRST
// bootstrap command to talk to a feed was the one command with no credentials. Both entry points
// now build their script here, so no dotnet command can miss it.
func TestBootstrapDockerScript(t *testing.T) {
	const envelope = "VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={}"
	cases := []struct {
		name        string
		argv        []string
		prefix      string
		dockerEnv   []string
		wantProv    bool
		wantPrefix  bool
		wantOrdered []string
	}{
		{
			name:        "dotnet build with an envelope installs the provider first",
			argv:        []string{"dotnet", "build", "App.Tests.csproj"},
			dockerEnv:   []string{envelope},
			wantProv:    true,
			wantOrdered: []string{"CredentialProvider", "dotnet"},
		},
		{
			name:      "dotnet build without an envelope does not",
			argv:      []string{"dotnet", "build", "App.Tests.csproj"},
			dockerEnv: nil,
			wantProv:  false,
		},
		{
			name:      "a non-dotnet command never gets it",
			argv:      []string{"npm", "install"},
			dockerEnv: []string{envelope},
			wantProv:  false,
		},
		{
			name:        "an explicit shell prefix still runs, after the provider install",
			argv:        []string{"dotnet", "test"},
			prefix:      "install-sdk.sh",
			dockerEnv:   []string{envelope},
			wantProv:    true,
			wantPrefix:  true,
			wantOrdered: []string{"CredentialProvider", "install-sdk.sh", "dotnet"},
		},
		{
			name:       "a shell prefix without an envelope is unchanged",
			argv:       []string{"dotnet", "test"},
			prefix:     "install-sdk.sh",
			wantProv:   false,
			wantPrefix: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := bootstrapDockerScript(tc.argv, tc.prefix, tc.dockerEnv)
			if got := strings.Contains(script, "CredentialProvider"); got != tc.wantProv {
				t.Fatalf("provider install present = %v, want %v:\n%s", got, tc.wantProv, script)
			}
			if tc.prefix != "" && strings.Contains(script, tc.prefix) != tc.wantPrefix {
				t.Fatalf("shell prefix present = %v, want %v:\n%s", !tc.wantPrefix, tc.wantPrefix, script)
			}
			if !strings.Contains(script, tc.argv[0]) {
				t.Fatalf("script lost the command %q:\n%s", tc.argv[0], script)
			}
			last := -1
			for _, token := range tc.wantOrdered {
				i := strings.Index(script, token)
				if i < 0 {
					t.Fatalf("script is missing %q:\n%s", token, script)
				}
				if i < last {
					t.Fatalf("%q appears out of order in:\n%s", token, script)
				}
				last = i
			}
		})
	}
}

// The build goal is the one that triggers the implicit restore, so its argv must be the one that
// carries the provider.
func TestDotnetGoalRunnerBuildArgv_isADotnetCommand(t *testing.T) {
	repo := t.TempDir()
	r := dotnetGoalRunner{repo: repo, csprojAbs: filepath.Join(repo, "tests", "App.Tests", "App.Tests.csproj")}
	argv, err := r.buildArgv()
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	if !bootstrapArgvIsDotnet(argv) {
		t.Fatalf("buildArgv = %v, which bootstrapDockerScript would not recognise as dotnet", argv)
	}
	script := bootstrapDockerScript(argv, "", []string{"VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={}"})
	if !strings.Contains(script, "CredentialProvider") {
		t.Fatalf("the build goal's script carries no credential provider:\n%s", script)
	}
}
