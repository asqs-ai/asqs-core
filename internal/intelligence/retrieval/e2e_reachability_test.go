package retrieval

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// twoTreeSolutionRepo is the monorepo shape that produced the failure: a root solution that lists
// only the root tree, beside a second, self-contained application the evaluator never builds.
func twoTreeSolutionRepo(t *testing.T) string {
	t.Helper()
	return writeReachRepo(t, map[string]string{
		"App.slnx": `<Solution>` +
			`<Project Path="src/Root.Web/Root.Web.csproj" />` +
			`<Project Path="tests/Root.Tests/Root.Tests.csproj" /></Solution>`,
		"src/Root.Web/Root.Web.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Root.Web/Api.cs":          "public class Api { }",
		"tests/Root.Tests/Root.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Root.Web\Root.Web.csproj" /></ItemGroup></Project>`,
		"tests/Root.Tests/ApiSpec.cs": "using Xunit;\npublic class ApiSpec { [Fact] public void A() { } }",

		"sample/src/Sample.Web/Sample.Web.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"sample/src/Sample.Web/Api.cs":            "public class Api { }",
		"sample/tests/Sample.Tests/Sample.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\Sample.Web\Sample.Web.csproj" /></ItemGroup></Project>`,
		"sample/tests/Sample.Tests/ApiSpec.cs": "using Xunit;\npublic class ApiSpec { [Fact] public void A() { } }",
	})
}

func e2ePlanOptions(root string) PlanOptions {
	return PlanOptions{
		Lang: "csharp", RepoID: "repo", RepoPath: root,
		MaxGapsE2E: 5, E2EFramework: "webapplicationfactory",
	}
}

func gapFiles(gaps []*TestGap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		if g != nil && g.Symbol != nil {
			out = append(out, g.Symbol.File)
		}
	}
	return out
}

// Run api-bdf7539b296a0df65a7cf1bf2bf2739b planned five E2E_SPEC gaps into a test project the
// evaluated solution does not list, and wrote five tests there. The compile and test steps name
// Clean.Architecture.slnx, so not one of them was ever built or run: a third of the run's output was
// invisible to the run's own verdict.
//
// The unit branch had consulted the reachability filter since the closure work; this branch never
// called it. Same filter, one loop over.
func TestListGapsE2E_skipsSpecsOutsideTheEvaluatedSolution(t *testing.T) {
	root := twoTreeSolutionRepo(t)
	meta := &mockGapMetaReader{
		testSymbols: []*metadata.Symbol{
			{ID: "root", Lang: "csharp", Kind: "E2E_SPEC", FQName: "E2E_SPEC:tests/Root.Tests/ApiSpec.cs", File: "tests/Root.Tests/ApiSpec.cs"},
			{ID: "sample", Lang: "csharp", Kind: "E2E_SPEC", FQName: "E2E_SPEC:sample/tests/Sample.Tests/ApiSpec.cs", File: "sample/tests/Sample.Tests/ApiSpec.cs"},
		},
		files: map[string]*metadata.File{
			"tests/Root.Tests/ApiSpec.cs":          {File: "tests/Root.Tests/ApiSpec.cs", IsTest: true},
			"sample/tests/Sample.Tests/ApiSpec.cs": {File: "sample/tests/Sample.Tests/ApiSpec.cs", IsTest: true},
		},
	}

	gaps, err := ListGapsE2E(context.Background(), meta, e2ePlanOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	files := gapFiles(gaps)
	if len(files) != 1 || files[0] != "tests/Root.Tests/ApiSpec.cs" {
		t.Fatalf("want only the solution-listed spec planned, got %v", files)
	}
}

// The API_ROUTE arm of the same branch has the same hole: a route under a tree no listed test
// project can reference yields an e2e test that is never compiled.
func TestListGapsE2E_skipsAPIRoutesOutsideTheEvaluatedSolution(t *testing.T) {
	root := twoTreeSolutionRepo(t)
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{
			{ID: "root", Lang: "csharp", Kind: "API_ROUTE", FQName: "GET /root", File: "src/Root.Web/Api.cs"},
			{ID: "sample", Lang: "csharp", Kind: "API_ROUTE", FQName: "GET /sample", File: "sample/src/Sample.Web/Api.cs"},
		},
		files: map[string]*metadata.File{
			"src/Root.Web/Api.cs":          {File: "src/Root.Web/Api.cs"},
			"sample/src/Sample.Web/Api.cs": {File: "sample/src/Sample.Web/Api.cs"},
		},
	}

	gaps, err := ListGapsE2E(context.Background(), meta, e2ePlanOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	files := gapFiles(gaps)
	if len(files) != 1 || files[0] != "src/Root.Web/Api.cs" {
		t.Fatalf("want only the solution-listed route planned, got %v", files)
	}
}

// Every language but C# gets a zero filter, which allows everything. Applying it in this branch
// must therefore be invisible to Java and TS/JS — a filter that narrowed their plans would remove
// work those runs should do, with nothing to say so.
func TestListGapsE2E_filterIsANoOpForOtherLanguages(t *testing.T) {
	root := twoTreeSolutionRepo(t)
	for _, tc := range []struct{ lang, file string }{
		{"java", "sample/src/main/java/app/ApiIT.java"},
		{"typescript", "sample/e2e/checkout.spec.ts"},
		{"javascript", "sample/e2e/cart.spec.js"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			meta := &mockGapMetaReader{
				testSymbols: []*metadata.Symbol{
					{ID: "s", Lang: tc.lang, Kind: "E2E_SPEC", FQName: "E2E_SPEC:" + tc.file, File: tc.file},
				},
				files: map[string]*metadata.File{tc.file: {File: tc.file, IsTest: true}},
			}
			opts := e2ePlanOptions(root)
			opts.Lang = tc.lang
			opts.E2EFramework = "playwright"

			gaps, err := ListGapsE2E(context.Background(), meta, opts)
			if err != nil {
				t.Fatal(err)
			}
			if files := gapFiles(gaps); len(files) != 1 || files[0] != tc.file {
				t.Fatalf("%s: a C#-only filter changed another language's plan, got %v", tc.lang, files)
			}
		})
	}
}
