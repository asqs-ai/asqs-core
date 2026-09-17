package retrieval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func sigWithVisibility(vis string) []byte {
	b, _ := json.Marshal(map[string]any{"visibility": vis, "exported": vis == "public"})
	return b
}

func csharpSym(file, fq, vis string) *metadata.Symbol {
	return &metadata.Symbol{
		ID: fq, Lang: "csharp", Kind: "method", FQName: fq, File: file,
		SignatureJSON: sigWithVisibility(vis),
	}
}

const sutCsproj = `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>`

func internalAccessRepo(t *testing.T, sutProj string) string {
	t.Helper()
	return writeReachRepo(t, map[string]string{
		"src/App.Core/App.Core.csproj": sutProj,
		"src/App.Core/LegacyReader.cs": "public class LegacyReader { internal static string Describe() { return \"\"; } }",
		"tests/App.Tests/App.Tests.csproj": reachTestCsprojHead +
			`<ProjectReference Include="..\..\src\App.Core\App.Core.csproj" /></ItemGroup></Project>`,
		"tests/App.Tests/LegacyReaderTests.cs": "using Xunit;\npublic class LegacyReaderTests { [Fact] public void A() { } }",
	})
}

// Run api-2555a79ee2660a8a5cd95c8c860090f5 planned a gap against an `internal` member from a test
// project in a different assembly. The generated test could not compile — CS0117 for three
// iterations — the fixer's only move was deleting tests, and fifty minutes and two of nine gaps
// went to a target that was never reachable.
//
// isPrivateMethod screens `private` and nothing screens `internal`, though across an assembly
// boundary the two are equally out of reach.
func TestInternalAccessFilter_dropsInternalWithoutAGrant(t *testing.T) {
	root := internalAccessRepo(t, sutCsproj)
	f := newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root})

	if f.allows(csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Describe()", "internal")) {
		t.Error("an internal member with no InternalsVisibleTo grant is not reachable from the test assembly")
	}
	if !f.allows(csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Read()", "public")) {
		t.Error("a public member must never be filtered")
	}
}

// The grant is what makes it reachable, in either of the two forms C# accepts.
func TestInternalAccessFilter_keepsInternalWhenGranted(t *testing.T) {
	item := `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><InternalsVisibleTo Include="App.Tests" /></ItemGroup></Project>`
	attr := sutCsproj // grant lives in source instead

	t.Run("msbuild item", func(t *testing.T) {
		root := internalAccessRepo(t, item)
		f := newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root})
		if !f.allows(csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Describe()", "internal")) {
			t.Error("an <InternalsVisibleTo> item grants access and the gap must stand")
		}
	})

	t.Run("assembly attribute", func(t *testing.T) {
		root := writeReachRepo(t, map[string]string{
			"src/App.Core/App.Core.csproj": attr,
			"src/App.Core/AssemblyInfo.cs": `[assembly: InternalsVisibleTo("App.Tests")]`,
			"src/App.Core/LegacyReader.cs": "public class LegacyReader { internal static string Describe() { return \"\"; } }",
			"tests/App.Tests/App.Tests.csproj": reachTestCsprojHead +
				`<ProjectReference Include="..\..\src\App.Core\App.Core.csproj" /></ItemGroup></Project>`,
		})
		f := newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root})
		if !f.allows(csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Describe()", "internal")) {
			t.Error("an [assembly: InternalsVisibleTo] attribute grants the same access")
		}
	})
}

// A repository whose tests live in the SAME project has no assembly boundary to cross, and internal
// is an ordinary call target there.
func TestInternalAccessFilter_keepsInternalInsideTheSameAssembly(t *testing.T) {
	root := writeReachRepo(t, map[string]string{
		"src/App/App.csproj":           reachTestCsprojHead + `</ItemGroup></Project>`,
		"src/App/LegacyReader.cs":      "public class LegacyReader { internal static string Describe() { return \"\"; } }",
		"src/App/LegacyReaderTests.cs": "using Xunit;\npublic class LegacyReaderTests { [Fact] public void A() { } }",
	})
	f := newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root})
	if !f.allows(csharpSym("src/App/LegacyReader.cs", "App.LegacyReader#Describe()", "internal")) {
		t.Error("with tests in the same project there is no assembly boundary to cross")
	}
}

// Every branch that cannot make the claim keeps the candidate: a wrong exclusion silently removes
// work the run should have done, and nothing reports it.
func TestInternalAccessFilter_keepsWhatItCannotJudge(t *testing.T) {
	root := internalAccessRepo(t, sutCsproj)
	internal := csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Describe()", "internal")

	if !newInternalAccessFilter(PlanOptions{Lang: "csharp"}).allows(internal) {
		t.Error("no repository path: nothing to read a grant from")
	}
	unknownFile := csharpSym("does/not/exist/Thing.cs", "T#M()", "internal")
	if !newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root}).allows(unknownFile) {
		t.Error("a file under no project must be kept")
	}
	noSig := &metadata.Symbol{ID: "x", Lang: "csharp", Kind: "method", File: "src/App.Core/LegacyReader.cs", FQName: "T#M()"}
	if !newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root}).allows(noSig) {
		t.Error("a row with no recorded visibility must be kept")
	}
}

// `internal` is a C# concept. Java's package-private member IS reachable from a test in the same
// package, which is where Java tests live, and TS/JS has no assemblies at all — so neither may be
// filtered by this rule.
func TestInternalAccessFilter_isCSharpOnly(t *testing.T) {
	root := internalAccessRepo(t, sutCsproj)
	for _, lang := range []string{"java", "typescript", "javascript"} {
		f := newInternalAccessFilter(PlanOptions{Lang: lang, RepoPath: root})
		sym := csharpSym("src/App.Core/LegacyReader.cs", "T#M()", "internal")
		sym.Lang = lang
		if !f.allows(sym) {
			t.Errorf("%s: this rule does not apply and must not filter", lang)
		}
	}
}

// protected internal is "this assembly OR derived", so a non-derived test in another assembly needs
// the same grant. private protected is "this assembly AND derived" and no grant alone opens it —
// but it is also already screened as private, so this rule must simply not claim it.
func TestInternalAccessFilter_twoWordAccessModifiers(t *testing.T) {
	root := internalAccessRepo(t, sutCsproj)
	f := newInternalAccessFilter(PlanOptions{Lang: "csharp", RepoPath: root})

	if f.allows(csharpSym("src/App.Core/LegacyReader.cs", "T#M()", "protected internal")) {
		t.Error("protected internal needs the grant too when the test does not derive from the type")
	}
	if !f.allows(csharpSym("src/App.Core/LegacyReader.cs", "T#M()", "protected")) {
		t.Error("protected is not an assembly-scoped modifier; this rule must not claim it")
	}
}

// The wiring, not just the predicate: ListGaps must drop the unreachable member and say so under
// its own reason, or a mis-tuned filter is invisible and the plan just silently gets smaller.
func TestListGaps_dropsUnreachableInternalsAndReportsTheReason(t *testing.T) {
	root := internalAccessRepo(t, sutCsproj)
	internal := csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#Describe(string)", "internal")
	public := csharpSym("src/App.Core/LegacyReader.cs", "App.Core.LegacyReader#ReadSkuList(string)", "public")
	for _, s := range []*metadata.Symbol{internal, public} {
		s.StartLine, s.EndLine = 10, 20 // a real body, so no span-derived rule fires first
	}

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{internal, public},
		files: map[string]*metadata.File{
			"src/App.Core/LegacyReader.cs": {File: "src/App.Core/LegacyReader.cs"},
		},
	}
	audit := &recordingPlanAuditor{}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{
		Lang: "csharp", RepoID: "repo", RepoPath: root, MaxGaps: 10, Audit: audit,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, g := range gaps {
		if g != nil && g.Symbol != nil {
			got = append(got, g.Symbol.FQName)
		}
	}
	if len(got) != 1 || got[0] != public.FQName {
		t.Fatalf("want only the public member planned, got %v", got)
	}
	if n := audit.reasonCount("plan.gaps_filtered_ineligible", IneligibleUnreachableInternal); n != 1 {
		t.Errorf("want 1 candidate dropped as %s, got %d", IneligibleUnreachableInternal, n)
	}
}
