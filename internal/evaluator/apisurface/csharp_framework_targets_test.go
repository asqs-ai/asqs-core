package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// tick is the generic-arity separator in an XML documentation ID. It is spelled this way because a
// literal backtick cannot appear inside a Go raw string, and every doc ID for a generic type has one.
const tick = "`"

// csRepoWithFrameworkDocs writes the XML documentation a real test project ships beside its
// assemblies: the xUnit attribute assembly, Moq, and the ASP.NET integration-testing package.
//
// The three shapes here are the three that a name-to-namespace resolver has to survive, and all
// three come from how .NET actually writes doc IDs rather than from how a model writes C#.
func csRepoWithFrameworkDocs(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	out := filepath.Join(repo, "bin", "Release", "net8.0")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		// `[Fact]` is sugar: the type is FactAttribute, and that is the only name in the doc file.
		"xunit.core.xml": `<?xml version="1.0"?>
<doc>
  <assembly><name>xunit.core</name></assembly>
  <members>
    <member name="T:Xunit.FactAttribute" />
    <member name="P:Xunit.FactAttribute.Skip" />
    <member name="P:Xunit.FactAttribute.DisplayName" />
    <member name="T:Xunit.TheoryAttribute" />
    <member name="T:Xunit.ITestOutputHelper" />
    <member name="M:Xunit.ITestOutputHelper.WriteLine(System.String)" />
  </members>
</doc>`,
		// A generic type carries its arity in the ID, so the bare name never matches literally.
		"Moq.xml": `<?xml version="1.0"?>
<doc>
  <assembly><name>Moq</name></assembly>
  <members>
    <member name="T:Moq.Mock` + tick + `1" />
    <member name="P:Moq.Mock` + tick + `1.Object" />
    <member name="M:Moq.Mock` + tick + `1.Setup(System.Object)" />
    <member name="M:Moq.Mock` + tick + `1.Verify" />
  </members>
</doc>`,
		"Microsoft.AspNetCore.Mvc.Testing.xml": `<?xml version="1.0"?>
<doc>
  <assembly><name>Microsoft.AspNetCore.Mvc.Testing</name></assembly>
  <members>
    <member name="T:Microsoft.AspNetCore.Mvc.Testing.WebApplicationFactory` + tick + `1" />
    <member name="M:Microsoft.AspNetCore.Mvc.Testing.WebApplicationFactory` + tick + `1.CreateClient" />
  </members>
</doc>`,
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// A C# attribute is written without its suffix — `[Fact]`, never `[FactAttribute]` — but the type,
// and therefore every doc ID naming it, carries the suffix. Matching the bare name literally found
// nothing, so the one list whose whole purpose is to tell the model where an attribute lives
// resolved to nothing for every attribute in it.
func TestCSharpProvider_resolvesAnAttributeWrittenWithoutItsSuffix(t *testing.T) {
	repo := csRepoWithFrameworkDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Fact"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Lookup(Fact) = %d surfaces, want 1: %+v", len(got), got)
	}
	if got[0].FQCN != "Xunit.FactAttribute" {
		t.Errorf("FQCN = %q, want Xunit.FactAttribute", got[0].FQCN)
	}
	if got[0].ImportHint != "using Xunit;" {
		t.Errorf("ImportHint = %q, want `using Xunit;`", got[0].ImportHint)
	}
}

// The suffix rule must not run backwards: a name the model spelled in full still resolves, and the
// rule adds a spelling rather than replacing one.
func TestCSharpProvider_resolvesAnAttributeWrittenInFull(t *testing.T) {
	repo := csRepoWithFrameworkDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "FactAttribute"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].FQCN != "Xunit.FactAttribute" {
		t.Fatalf("Lookup(FactAttribute) = %+v, want one Xunit.FactAttribute", got)
	}
}

// `Mock<T>` is written with its argument and documented with its arity. Neither spelling is the
// bare name, so the arity has to be dropped before the comparison.
func TestCSharpProvider_resolvesAGenericTypeByItsBareName(t *testing.T) {
	repo := csRepoWithFrameworkDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Mock"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Lookup(Mock) = %d surfaces, want 1: %+v", len(got), got)
	}
	if got[0].ImportHint != "using Moq;" {
		t.Errorf("ImportHint = %q, want `using Moq;`", got[0].ImportHint)
	}
	if len(got[0].Members) == 0 {
		t.Error("resolved the type but dumped no members")
	}
}

// The framework list is what puts an import in front of the model BEFORE it writes the file. C#
// had none: PregenerateTargets returned only the E2E targets, so a unit gap — the common case —
// got an empty block and the model guessed the namespace for every attribute it wrote.
func TestPregenerateTargets_csharpCarriesFrameworkTypes(t *testing.T) {
	got := PregenerateTargets("csharp", "", false)
	if len(got) == 0 {
		t.Fatal("a C# unit gap resolved no pregenerate targets at all")
	}
	want := map[string]bool{"Fact": false, "Test": false, "TestMethod": false, "Mock": false, "WebApplicationFactory": false}
	for _, tg := range got {
		if _, ok := want[tg.Name]; ok {
			want[tg.Name] = true
			if tg.Kind != KindSymbol {
				t.Errorf("target %s has kind %q, want KindSymbol — the namespace is the unknown", tg.Name, tg.Kind)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("framework target %q is missing", name)
		}
	}
}

// A bare BCL type would be resolved from whatever assembly happens to document it, and its member
// list is both enormous and already known to the model. Spending the budget there is what crowds
// out the types it gets wrong.
func TestPregenerateTargets_csharpOmitsWellKnownBCLTypes(t *testing.T) {
	for _, tg := range PregenerateTargets("csharp", "", false) {
		if tg.Name == "HttpClient" || tg.Name == "String" || tg.Name == "Task" {
			t.Errorf("target %q is a BCL type the model already knows", tg.Name)
		}
	}
}

// Canonical imports are the half of the contract that can tell one framework version from another.
// xUnit moved ITestOutputHelper out of Xunit.Abstractions in v3, which is the same class of
// relocation the Java side exists to catch — and the C# arm resolved nothing at all.
func TestResolveCanonicalImports_csharp(t *testing.T) {
	repo := csRepoWithFrameworkDocs(t)
	got := ResolveCanonicalImports(context.Background(), NewCSharpProvider(), repo, "csharp")
	if len(got) == 0 {
		t.Fatal("no canonical imports resolved for C#")
	}
	if got["Fact"] != "Xunit.FactAttribute" {
		t.Errorf("Fact -> %q, want Xunit.FactAttribute", got["Fact"])
	}
	if got["ITestOutputHelper"] != "Xunit.ITestOutputHelper" {
		t.Errorf("ITestOutputHelper -> %q, want Xunit.ITestOutputHelper", got["ITestOutputHelper"])
	}
	// Absent from this project's packages: saying nothing is the only honest answer.
	if v, ok := got["TestMethod"]; ok {
		t.Errorf("TestMethod resolved to %q in a project with no MSTest package", v)
	}
}
