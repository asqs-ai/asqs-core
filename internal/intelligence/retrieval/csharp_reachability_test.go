package retrieval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeReachRepo(t *testing.T, files map[string]string) string {
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

const reachTestCsprojHead = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
	`<PackageReference Include="xunit" Version="2.9.0" />`

// A wrong exclusion here removes work the run should have done and nothing reports it, so a closure
// that cannot be read in full has to disable the filter rather than shrink it.
//
// Taking the union across test projects made that fail again as soon as a solution had two of them —
// the normal shape once a solution has more than one component, and the shape of the validation
// fixture. One project's closure came back unreadable, the other's did not, and every gap under the
// unreadable one's production code was silently excluded.
func TestCSharpReachableFilter_oneUnreadableClosureDisablesTheFilter(t *testing.T) {
	files := map[string]string{
		"src/Core/Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Core/Calc.cs":     "public class Calc { }",
		"src/Web/Web.csproj":   `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Web/Page.cs":      "public class Page { }",
		"tests/CoreT/CoreT.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Core\Core.csproj" /></ItemGroup></Project>`,
		// This one's reference cannot be resolved without MSBuild's property bag.
		"tests/WebT/WebT.csproj": reachTestCsprojHead +
			`<ProjectReference Include="$(SrcRoot)\Web\Web.csproj" /></ItemGroup></Project>`,
	}
	root := writeReachRepo(t, files)

	f := csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: root})
	for _, file := range []string{"src/Core/Calc.cs", "src/Web/Page.cs"} {
		if !f.allows(file) {
			t.Errorf("%s was filtered out although one test project's closure could not be read", file)
		}
	}

	// With every closure readable the filter still does its job: Web is referenced by nothing.
	files["tests/WebT/WebT.csproj"] = reachTestCsprojHead +
		`<ProjectReference Include="..\..\src\Core\Core.csproj" /></ItemGroup></Project>`
	root = writeReachRepo(t, files)
	f = csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: root})
	if !f.allows("src/Core/Calc.cs") {
		t.Error("src/Core is referenced by both test projects and must be reachable")
	}
	if f.allows("src/Web/Page.cs") {
		t.Error("src/Web is referenced by nothing and should have been filtered out")
	}
}
