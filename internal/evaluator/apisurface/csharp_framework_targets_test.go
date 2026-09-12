package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// The framework group is ALL bare names, which left `wanted` — the namespace set the packages-folder
// search iterates — completely empty. Step 1 walks the repository's build output; step 2 never ran
// at all, so a project whose bin/ carries no XML documentation resolved nothing.
//
// Run run-1789245041122 against the NUnit fixture is the case: nunit.framework.xml sat in the
// package cache the whole time, and the block reported "no NuGet XML documentation found for Fact,
// Theory, Test, …" having never looked there.
//
// The narrowing that replaces it is the repository's own package closure: exactly the packages this
// project references, which is both precise and already resolved by dotnetproj.
func TestCSharpProvider_searchesThePackageCacheForBareNames(t *testing.T) {
	repo := t.TempDir()
	// A project referencing NUnit, with NO build output of its own.
	if err := os.WriteFile(filepath.Join(repo, "Shop.Tests.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup><PackageReference Include="NUnit" Version="4.2.2" /></ItemGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stand-in package cache, laid out the way NuGet lays one out.
	cache := t.TempDir()
	docDir := filepath.Join(cache, "nunit", "4.2.2", "lib", "net6.0")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docDir, "nunit.framework.xml"), []byte(`<?xml version="1.0"?>
<doc>
  <assembly><name>nunit.framework</name></assembly>
  <members>
    <member name="T:NUnit.Framework.TestAttribute" />
    <member name="T:NUnit.Framework.Assert" />
    <member name="M:NUnit.Framework.Assert.That(System.Object)" />
  </members>
</doc>`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NUGET_PACKAGES", cache)

	p := NewCSharpProvider()
	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Test"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].FQCN != "NUnit.Framework.TestAttribute" {
		t.Fatalf("Lookup(Test) = %+v, want one NUnit.Framework.TestAttribute", got)
	}
	if got[0].ImportHint != "using NUnit.Framework;" {
		t.Errorf("ImportHint = %q, want `using NUnit.Framework;`", got[0].ImportHint)
	}
}

// The closure is the bound. A package the repository does not reference must not be read, or the
// search degenerates into a crawl of a cache that holds every version of everything.
func TestCSharpProvider_bareNameSearchStaysInsideThePackageClosure(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "App.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup><PackageReference Include="NUnit" Version="4.2.2" /></ItemGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	for pkg, member := range map[string]string{
		"nunit":   "T:NUnit.Framework.TestAttribute",
		"someone": "T:Someone.TestAttribute",
	} {
		dir := filepath.Join(cache, pkg, "1.0.0", "lib", "net8.0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `<?xml version="1.0"?><doc><assembly><name>` + pkg + `</name></assembly><members><member name="` + member + `" /></members></doc>`
		if err := os.WriteFile(filepath.Join(dir, pkg+".xml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NUGET_PACKAGES", cache)

	got, err := NewCSharpProvider().Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Test"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	for _, s := range got {
		if strings.HasPrefix(s.FQCN, "Someone.") {
			t.Errorf("read %s from a package this repository does not reference", s.FQCN)
		}
	}
}

// A documented type with NO members never entered the index: parseDocMemberID drops every `T:`
// entry, and only member IDs created a key. Attributes are exactly the types that have no members
// worth documenting — [Test], [Theory], [TestMethod] — and resolving them is not about members at
// all. It is about printing the namespace, which is what the import line needs.
func TestCSharpProvider_resolvesATypeThatDocumentsNoMembers(t *testing.T) {
	repo := t.TempDir()
	out := filepath.Join(repo, "bin", "Release", "net8.0")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "nunit.framework.xml"), []byte(`<?xml version="1.0"?>
<doc>
  <assembly><name>nunit.framework</name></assembly>
  <members>
    <member name="T:NUnit.Framework.TestAttribute" />
    <member name="T:NUnit.Framework.Assert" />
    <member name="M:NUnit.Framework.Assert.That(System.Object)" />
  </members>
</doc>`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := NewCSharpProvider().Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Test"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].FQCN != "NUnit.Framework.TestAttribute" {
		t.Fatalf("Lookup(Test) = %+v, want one NUnit.Framework.TestAttribute", got)
	}
	if got[0].ImportHint != "using NUnit.Framework;" {
		t.Errorf("ImportHint = %q, want `using NUnit.Framework;`", got[0].ImportHint)
	}
	// A zero-member surface is the point: RenderSurfaces emits it as an import line rather than a
	// member dump, which is the fact the model is missing.
	if len(got[0].Members) != 0 {
		t.Errorf("Members = %v, want none for a type that documents none", got[0].Members)
	}
}
