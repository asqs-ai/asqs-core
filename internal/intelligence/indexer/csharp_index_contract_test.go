package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The C# indexer emits a per-file unresolved_invocations count, and the Go contract dropped it: the
// run reported one repository-wide total, so "19 unresolved" named no file and pointed at nothing
// to fix. A file whose calls do not resolve is a file whose project references are incomplete, and
// that is a per-file fact.
func TestLangIndexerJSON_carriesPerFileUnresolvedInvocations(t *testing.T) {
	const line = `{"path":"src/Basket.cs","lang":"csharp","module":"Shop.Core","is_test":false,
	  "symbols":[],"edges":[],"unresolved_invocations":7}`
	var got LangIndexerJSON
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UnresolvedInvocations == nil {
		t.Fatal("unresolved_invocations was dropped by the contract")
	}
	if *got.UnresolvedInvocations != 7 {
		t.Errorf("UnresolvedInvocations = %d, want 7", *got.UnresolvedInvocations)
	}
	// Absent is distinguishable from zero: an indexer that does not report the count must not be
	// read as reporting a clean file.
	var none LangIndexerJSON
	if err := json.Unmarshal([]byte(`{"path":"a.cs","lang":"csharp"}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.UnresolvedInvocations != nil {
		t.Errorf("UnresolvedInvocations = %v for a line that omits it, want nil", *none.UnresolvedInvocations)
	}
}

// The four C# structural edges the indexer can emit had no registry entry, so each scored the
// unregistered default — above the ambient types it should rank beneath. An edge saying "this
// method accepts this type" is weaker evidence than a call, and the registry is where that is said.
func TestEdgeTypes_csharpStructuralEdgesAreRegistered(t *testing.T) {
	for _, name := range []string{"ACCEPTS_PARAM_TYPE", "RETURNS_TYPE", "READS_FIELD", "WRITES_FIELD"} {
		entry, ok := EdgeTypes[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if entry.Confidence >= EdgeConfidenceDirect {
			t.Errorf("%s scores %d, want below a direct call (%d)", name, entry.Confidence, EdgeConfidenceDirect)
		}
		if entry.Description == "" {
			t.Errorf("%s has no description", name)
		}
	}
}

// Package versions were read with one regex requiring Version as an ATTRIBUTE in a fixed order.
// Three shapes a real solution uses were invisible, and every one of them means the XML
// documentation for that package is never found — so the API surface has nothing to resolve against.
func TestParseCsprojPackageRefs_findsEveryVersionForm(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Directory.Packages.props", `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup><PackageVersion Include="NUnit" Version="4.2.2" /></ItemGroup>
</Project>`)
	write("src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Serilog" Version="4.0.0" />
    <PackageReference Version="8.0.4" Include="Microsoft.EntityFrameworkCore" />
    <PackageReference Include="Moq"><Version>4.20.70</Version></PackageReference>
    <PackageReference Include="NUnit" />
  </ItemGroup>
</Project>`)

	got := map[string]string{}
	for _, r := range parseCsprojPackageRefs(repo) {
		got[r.id] = r.version
	}
	for id, want := range map[string]string{
		"Serilog":                       "4.0.0",   // the shape that already worked
		"Microsoft.EntityFrameworkCore": "8.0.4",   // attributes in the other order
		"Moq":                           "4.20.70", // Version as a child element
		"NUnit":                         "4.2.2",   // central package management
	} {
		if got[id] != want {
			t.Errorf("%s = %q, want %q (all refs: %v)", id, got[id], want, got)
		}
	}
}

// The total alone points at nothing to fix. The worst offenders are what somebody can act on.
func TestWorstUnresolvedFiles(t *testing.T) {
	n := func(v int) *int { return &v }
	parsed := map[string]*ParsedFile{
		"src/A.cs":   {UnresolvedInvocations: n(3)},
		"src/B.cs":   {UnresolvedInvocations: n(11)},
		"src/C.cs":   {UnresolvedInvocations: n(0)},
		"src/D.cs":   {UnresolvedInvocations: nil}, // not measured
		"src/E.cs":   {UnresolvedInvocations: n(3)},
		"src/Nil.cs": nil,
	}
	got := WorstUnresolvedFiles(parsed, 3)
	want := []string{"src/B.cs (11)", "src/A.cs (3)", "src/E.cs (3)"}
	if len(got) != len(want) {
		t.Fatalf("WorstUnresolvedFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("WorstUnresolvedFiles[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// A file that reports nothing is not a clean file, and a file with zero is not worth naming.
	for _, unwanted := range []string{"src/C.cs", "src/D.cs", "src/Nil.cs"} {
		for _, g := range WorstUnresolvedFiles(parsed, 10) {
			if len(g) >= len(unwanted) && g[:len(unwanted)] == unwanted {
				t.Errorf("named %q, which reports no unresolved invocations", unwanted)
			}
		}
	}
	if got := WorstUnresolvedFiles(nil, 5); got != nil {
		t.Errorf("WorstUnresolvedFiles(nil) = %v, want nil", got)
	}
}
