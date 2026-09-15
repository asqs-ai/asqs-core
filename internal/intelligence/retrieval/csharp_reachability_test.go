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

// A test project the EVALUATOR never builds cannot cover anything, however well it can reference
// the source. The compile and test steps both name the root solution, so a project outside it is
// never compiled and never run — and a gap planned for it produces a test that buys nothing.
//
// A validation run is the case. Twelve of its fourteen generated tests went
// to sample/tests/NimblePros.SampleToDo.FunctionalTests, correctly — that is the only project able
// to reference sample/src. The root solution does not list it, so the test run covered three DLLs
// from the root tree and the sample project appeared nowhere. Fourteen gaps "succeeded" and twelve
// of them ran nothing.
func TestCSharpReachableFilter_ignoresTestProjectsOutsideTheRootSolution(t *testing.T) {
	files := map[string]string{
		// The root solution lists the root tree and nothing under sample/.
		"App.slnx": `<Solution>` +
			`<Project Path="src/Root.Core/Root.Core.csproj" />` +
			`<Project Path="tests/Root.Tests/Root.Tests.csproj" /></Solution>`,
		"src/Root.Core/Root.Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Root.Core/Calc.cs":          "public class Calc { }",
		"tests/Root.Tests/Root.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Root.Core\Root.Core.csproj" /></ItemGroup></Project>`,
		// A second, self-contained tree the evaluator never touches.
		"sample/src/Sample.Core/Sample.Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"sample/src/Sample.Core/Handler.cs":         "public class Handler { }",
		"sample/tests/Sample.Tests/Sample.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Sample.Core\Sample.Core.csproj" /></ItemGroup></Project>`,
	}
	root := writeReachRepo(t, files)

	f := csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: root})
	if !f.allows("src/Root.Core/Calc.cs") {
		t.Error("root-tree source is reachable from a solution-listed test project and must be planned")
	}
	if f.allows("sample/src/Sample.Core/Handler.cs") {
		t.Error("sample/ source is only reachable from a project the solution does not list; planning it produces a test that never runs")
	}
}

// With no root solution there is nothing to restrict against, so the filter behaves exactly as it
// did: a repository laid out without one is not one this can reason about.
func TestCSharpReachableFilter_withoutARootSolutionRestrictsNothing(t *testing.T) {
	root := writeReachRepo(t, map[string]string{
		"sample/src/Sample.Core/Sample.Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"sample/src/Sample.Core/Handler.cs":         "public class Handler { }",
		"sample/tests/Sample.Tests/Sample.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Sample.Core\Sample.Core.csproj" /></ItemGroup></Project>`,
	})
	if f := csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: root}); !f.allows("sample/src/Sample.Core/Handler.cs") {
		t.Error("with no solution to consult, the previous behaviour must stand")
	}
}

// A solution that lists no test project at all is the bootstrap case — one is about to be created
// and added to it — and everything stays reachable, as it does when no test project exists.
func TestCSharpReachableFilter_solutionWithNoTestProjectIsTheBootstrapCase(t *testing.T) {
	root := writeReachRepo(t, map[string]string{
		"App.slnx":                       `<Solution><Project Path="src/Root.Core/Root.Core.csproj" /></Solution>`,
		"src/Root.Core/Root.Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Root.Core/Calc.cs":          "public class Calc { }",
		"sample/tests/Sample.Tests/Sample.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Root.Core\Root.Core.csproj" /></ItemGroup></Project>`,
	})
	if f := csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: root}); !f.allows("src/Root.Core/Calc.cs") {
		t.Error("a solution with no test project yet must not filter everything out")
	}
}
