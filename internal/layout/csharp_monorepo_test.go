package layout

import (
	"path/filepath"
	"testing"
)

func toSlash(p string) string { return filepath.ToSlash(p) }

// A mono-repo holds several independent .NET trees, and a test can only reference the projects its
// own project references. Choosing ONE test project for the whole repository puts a test for tree B
// into tree A, where neither the type under test nor its namespace exists.
//
// A validation run: seven unit tests for sample/src/NimblePros.SampleToDo.*
// were written into tests/Clean.Architecture.FunctionalTests, the root tree's project. They could
// not compile, and the fixer spent eight rounds on it — the model alternated between the real
// NimblePros namespace (which that project cannot reference) and an invented Clean.Architecture one
// (which does not exist), and ASQS rejected the second as an unresolved dependency every time.
func TestSuggestedCSharpUnitTestPath_routesToTheProjectThatCanReferenceTheSource(t *testing.T) {
	const rootTests = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
		`<PackageReference Include="xunit" Version="2.9.2" />` +
		`<ProjectReference Include="..\..\src\Root.Core\Root.Core.csproj" /></ItemGroup></Project>`
	const sampleTests = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>` +
		`<PackageReference Include="xunit" Version="2.9.2" />` +
		`<ProjectReference Include="..\..\src\Sample.UseCases\Sample.UseCases.csproj" /></ItemGroup></Project>`
	const lib = `<Project Sdk="Microsoft.NET.Sdk"></Project>`

	root := writeSolutionTree(t, map[string]string{
		"Root.slnx": `<Solution>` +
			`<Project Path="src/Root.Core/Root.Core.csproj" />` +
			`<Project Path="tests/Root.FunctionalTests/Root.FunctionalTests.csproj" /></Solution>`,
		"src/Root.Core/Root.Core.csproj":                                    lib,
		"src/Root.Core/Contributor.cs":                                      "public class Contributor { }",
		"tests/Root.FunctionalTests/Root.FunctionalTests.csproj":            rootTests,
		"sample/src/Sample.UseCases/Sample.UseCases.csproj":                 lib,
		"sample/src/Sample.UseCases/AddToDoItemHandler.cs":                  "public class AddToDoItemHandler { }",
		"sample/tests/Sample.FunctionalTests/Sample.FunctionalTests.csproj": sampleTests,
		// The bootstrap verified the ROOT tree's project, which is right for root sources and
		// wrong for everything under sample/.
		".asqs/test-stack.json": `{"version":1,"language":"csharp","runner":"xunit",` +
			`"test_project":"tests/Root.FunctionalTests/Root.FunctionalTests.csproj","verified":true}`,
	})

	got := SuggestedCSharpUnitTestPath("sample/src/Sample.UseCases/AddToDoItemHandler.cs", root)
	if want := "sample/tests/Sample.FunctionalTests/AddToDoItemHandlerTests.cs"; toSlash(got) != want {
		t.Errorf("a test for sample/ code went to %q; want %q", toSlash(got), want)
	}

	got = SuggestedCSharpUnitTestPath("src/Root.Core/Contributor.cs", root)
	if want := "tests/Root.FunctionalTests/ContributorTests.cs"; toSlash(got) != want {
		t.Errorf("a test for root code went to %q; want %q", toSlash(got), want)
	}
}
