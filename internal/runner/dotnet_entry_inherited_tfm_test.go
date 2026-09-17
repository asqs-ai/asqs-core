package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTFMTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The fallback pins /p:TargetFramework=<configured> for a project MSBuild cannot resolve one for.
// Asking the .csproj FILE answers a different question: a repository that declares its framework
// once, in Directory.Build.props, read as declaring none — so the run pinned the configured value
// over the framework the project actually restores for, and the build failed on NETSDK1005
// ("Assets file … doesn't have a target for 'net8.0'") before a single test was generated.
func TestProjectResolvesConcreteTargetFramework_readsTheInheritedValue(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name: "declared in the project itself",
			files: map[string]string{
				"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup>` +
					`<TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
			},
			want: true,
		},
		{
			name: "inherited from Directory.Build.props at the repository root",
			files: map[string]string{
				"Directory.Build.props": `<Project><PropertyGroup>` +
					`<TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>`,
				"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			},
			want: true,
		},
		{
			name: "inherited from a nearer Directory.Build.props",
			files: map[string]string{
				"src/Directory.Build.props": `<Project><PropertyGroup>` +
					`<TargetFrameworks>net8.0;net10.0</TargetFrameworks></PropertyGroup></Project>`,
				"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			},
			want: true,
		},
		{
			name: "nothing declares one anywhere: the fallback is what it is for",
			files: map[string]string{
				"Directory.Build.props": `<Project><PropertyGroup><Nullable>enable</Nullable></PropertyGroup></Project>`,
				"src/App/App.csproj":    `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			},
			want: false,
		},
		{
			name: "an unexpanded property reference is not a concrete framework",
			files: map[string]string{
				"Directory.Build.props": `<Project><PropertyGroup>` +
					`<TargetFramework>$(DefaultTfm)</TargetFramework></PropertyGroup></Project>`,
				"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTFMTree(t, tc.files)
			got, err := ProjectResolvesConcreteTargetFramework(root, filepath.Join(root, "src", "App", "App.csproj"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ProjectResolvesConcreteTargetFramework = %v; want %v", got, tc.want)
			}
		})
	}
}
