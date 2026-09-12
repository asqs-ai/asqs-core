package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// csRepoWithDocs copies the Playwright doc fixture into a temp repo and adds a second assembly, so
// simple-name resolution has to choose between assemblies rather than read the only one present.
func csRepoWithDocs(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	out := filepath.Join(repo, "bin", "Release", "net8.0")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "cs_repo", "bin", "Release", "net8.0", "Microsoft.Playwright.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "Microsoft.Playwright.xml"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	const xunitDoc = `<?xml version="1.0"?>
<doc>
  <assembly><name>xunit.assert</name></assembly>
  <members>
    <member name="T:Xunit.Assert" />
    <member name="M:Xunit.Assert.Equal(System.Object,System.Object)" />
    <member name="M:Xunit.Assert.True(System.Boolean)" />
    <member name="M:Xunit.Assert.NotNull(System.Object)" />
    <member name="M:Xunit.Assert.Throws(System.Action)" />
  </members>
</doc>`
	if err := os.WriteFile(filepath.Join(out, "xunit.assert.xml"), []byte(xunitDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// A diagnostic names a type by its SIMPLE name — `'Assert' does not contain a definition for 'Equl'`
// — and the whole point of resolving it is that the model does not know the namespace.
// candidateDocFiles derived its search from the dotted prefix of the target name, so a bare name
// produced no namespaces to match, no candidate files, and the provider returned
// "no NuGet XML documentation found". Every bare-name target ended in fix_api_surface_unavailable.
func TestCSharpProvider_resolvesABareSymbolName(t *testing.T) {
	repo := csRepoWithDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Assert"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("a bare symbol name resolved to nothing")
	}
	s := got[0]
	if s.FQCN != "Xunit.Assert" {
		t.Errorf("FQCN = %q, want Xunit.Assert", s.FQCN)
	}
	joined := strings.Join(s.Members, " ") + " " + strings.Join(s.AllMemberNames, " ")
	for _, want := range []string{"Equal", "True", "NotNull"} {
		if !strings.Contains(joined, want) {
			t.Errorf("member %q missing from the surface: %+v", want, s)
		}
	}
}

// The point of resolving the type is to state the using line that makes it compile.
func TestCSharpProvider_bareSymbolCarriesTheUsingLine(t *testing.T) {
	repo := csRepoWithDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "ILocatorAssertions"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("nothing resolved")
	}
	rendered := RenderSurfaces(got)
	if !strings.Contains(rendered, "using Microsoft.Playwright;") {
		t.Errorf("the rendered surface does not state the using line:\n%s", rendered)
	}
}

// A name that appears in two assemblies is ambiguous, and ambiguity is a fact worth stating rather
// than a coin to flip: picking one silently is how a model ends up with the wrong using.
func TestCSharpProvider_ambiguousBareSymbolNamesAllCandidates(t *testing.T) {
	repo := csRepoWithDocs(t)
	out := filepath.Join(repo, "bin", "Release", "net8.0")
	const otherDoc = `<?xml version="1.0"?>
<doc>
  <assembly><name>NUnit.Framework</name></assembly>
  <members>
    <member name="T:NUnit.Framework.Assert" />
    <member name="M:NUnit.Framework.Assert.That(System.Object)" />
  </members>
</doc>`
	if err := os.WriteFile(filepath.Join(out, "NUnit.Framework.xml"), []byte(otherDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Assert"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("an ambiguous name produced %d surface(s), want both candidates: %+v", len(got), got)
	}
	fqcns := map[string]bool{}
	for _, s := range got {
		fqcns[s.FQCN] = true
	}
	for _, want := range []string{"Xunit.Assert", "NUnit.Framework.Assert"} {
		if !fqcns[want] {
			t.Errorf("candidate %q not offered; got %v", want, fqcns)
		}
	}
}

// A dotted name still takes the existing path, which narrows by assembly rather than scanning.
func TestCSharpProvider_dottedNameUnchanged(t *testing.T) {
	repo := csRepoWithDocs(t)
	p := NewCSharpProvider()

	got, err := p.Lookup(context.Background(), repo,
		[]Target{{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions", Member: "ToContainTextAsync"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].FQCN != "Microsoft.Playwright.ILocatorAssertions" {
		t.Fatalf("dotted lookup changed: %+v", got)
	}
}

// A name in no assembly resolves to nothing, and that is not an error: the provider's absence
// contract is that an empty result says nothing at all.
func TestCSharpProvider_unknownBareNameIsSilent(t *testing.T) {
	repo := csRepoWithDocs(t)
	p := NewCSharpProvider()

	got, _ := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "NoSuchTypeAnywhere"}})
	for _, s := range got {
		if strings.Contains(s.FQCN, "NoSuchTypeAnywhere") {
			t.Fatalf("invented a resolution: %+v", s)
		}
	}
}

// The type is exported, so a zero value is a shape a caller can construct. It used to panic on the
// first document it parsed.
func TestCSharpProvider_zeroValueDoesNotPanic(t *testing.T) {
	repo := csRepoWithDocs(t)
	var p CSharpProvider
	if _, err := p.Lookup(context.Background(), repo, []Target{{Kind: KindSymbol, Name: "Assert"}}); err != nil {
		t.Fatalf("Lookup on a zero-value provider: %v", err)
	}
}
