package dotnetproj

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
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

const reachProdCsproj = `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`

// The closure this returns decides which production code the planner is allowed to plan gaps in. A
// reference it cannot see removes every gap in the project that reference points at, and nothing
// reports the work that was never done — so an incomplete reading must produce no claim at all
// rather than a short list.
func TestTestProjectReachableDirs_incompleteClosureMakesNoClaim(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantNil   bool
		wantHasSr bool
	}{
		{
			name: "a literal ProjectReference in the test project is the resolved case",
			files: map[string]string{
				"src/Shop/Shop.csproj": reachProdCsproj,
				"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
					`<ProjectReference Include="..\..\src\Shop\Shop.csproj" /></ItemGroup></Project>`,
			},
			wantHasSr: true,
		},
		{
			name: "a ProjectReference factored into a shared Directory.Build.props is inherited",
			files: map[string]string{
				"src/Shop/Shop.csproj":               reachProdCsproj,
				"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
				// Relative to the PROJECT directory, which is what MSBuild resolves an imported
				// file's item paths against — not to the props file's own directory.
				"tests/Directory.Build.props": `<Project><ItemGroup>` +
					`<ProjectReference Include="..\..\src\Shop\Shop.csproj" /></ItemGroup></Project>`,
			},
			wantHasSr: true,
		},
		{
			name: "an Include built from an MSBuild property cannot be resolved here",
			files: map[string]string{
				"src/Shop/Shop.csproj": reachProdCsproj,
				"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
					`<ProjectReference Include="$(SrcRoot)\Shop\Shop.csproj" /></ItemGroup></Project>`,
			},
			wantNil: true,
		},
		{
			name: "an Include that does not resolve to a file on disk",
			files: map[string]string{
				"src/Shop/Shop.csproj": reachProdCsproj,
				"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
					`<ProjectReference Include="..\..\src\Gone\Gone.csproj" /></ItemGroup></Project>`,
			},
			wantNil: true,
		},
		{
			name: "source linked in from outside the project directory",
			files: map[string]string{
				"src/Shop/Shop.csproj":   reachProdCsproj,
				"src/Shop/Calculator.cs": "public class Calculator { }",
				"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
					`<Compile Include="..\..\src\Shop\Calculator.cs" Link="Calculator.cs" /></ItemGroup></Project>`,
			},
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, tc.files)
			got := TestProjectReachableDirs(root, "tests/Shop.Tests/Shop.Tests.csproj")
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected no claim for an unresolvable closure; got %v", got)
				}
				return
			}
			if tc.wantHasSr && !PathIsReachableFrom("src/Shop/Calculator.cs", got) {
				t.Fatalf("src/Shop is referenced but not in the closure %v", got)
			}
		})
	}
}
