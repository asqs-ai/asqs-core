package csharpindexer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

// minimalFixtureRepo is the checked-in fixture under testdata/minimal. It is a real project rather
// than a temp-dir construction because the point is to prove what Roslyn produces for shapes that
// are easy to get wrong from a description.
func minimalFixtureRepo(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "minimal"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// Five kinds of C# declaration produced no symbol at all, and each absence has the same
// consequence: the plan cannot propose a gap against something it cannot see, and the fixer cannot
// state a fact about it.
//
// The enum is the starkest. EnumDeclarationSyntax is a BaseTypeDeclarationSyntax and NOT a
// TypeDeclarationSyntax, so the walk over type declarations never visited one — every enum in every
// C# repository indexed to nothing.
func TestIndexer_emitsEveryDeclarationKind(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), minimalFixtureRepo(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	shapes := m["src/Shapes.cs"]
	if shapes == nil {
		t.Fatalf("src/Shapes.cs not indexed; got %v", keysOf(m))
	}
	byFQ := map[string]string{} // fq name -> kind
	for _, s := range shapes.Symbols {
		byFQ[s.FQName] = s.Kind
	}
	want := map[string]string{
		"Minimal.Domain.BasketState":                           "enum",
		"Minimal.Domain.BasketState#Open":                      "field",
		"Minimal.Domain.BasketState#CheckedOut":                "field",
		"Minimal.Domain.BasketLine":                            "record",
		"Minimal.Domain.BasketLine#Sku":                        "property", // positional: nothing in the body declares it
		"Minimal.Domain.BasketLine#Quantity":                   "property",
		"Minimal.Domain.BasketChanged":                         "delegate",
		"Minimal.Domain.Basket#this[int]":                      "property", // an indexer
		"Minimal.Domain.Basket#op_Addition(Basket,BasketLine)": "method",   // an operator
	}
	for fq, kind := range want {
		got, ok := byFQ[fq]
		if !ok {
			t.Errorf("%s (%s) was not indexed", fq, kind)
			continue
		}
		if got != kind {
			t.Errorf("%s indexed as %q, want %q", fq, got, kind)
		}
	}
}

// A file with no namespace declaration had no MODULE, so its symbols had no container: every
// CONTAINS edge was dropped and chunking had nothing to group by. Top-level statements are the
// common case — Program.cs in every modern ASP.NET template declares no namespace — and it is the
// entry point, so losing it loses the file that wires the application together.
func TestIndexer_topLevelStatementFileGetsAModule(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), minimalFixtureRepo(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	prog := m["src/Program.cs"]
	if prog == nil {
		t.Fatalf("src/Program.cs not indexed; got %v", keysOf(m))
	}
	if prog.Module == "" {
		t.Error("module is empty for a top-level-statement file")
	}
	found := false
	for _, s := range prog.Symbols {
		if s.Kind == "MODULE" {
			found = true
		}
	}
	if !found {
		t.Errorf("no MODULE symbol; symbols = %+v", prog.Symbols)
	}
}

// A generated file's symbols are real, so they compete for plan budget and prompt space with the
// hand-written code the run exists to test — and nothing may edit them, because the generator will
// overwrite them.
func TestIndexer_skipsGeneratedFiles(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), minimalFixtureRepo(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for path := range m {
		if strings.Contains(strings.ToLower(path), ".designer.cs") {
			t.Errorf("indexed a generated file: %s", path)
		}
	}
}

// Every consumer downstream — retrieval's context builder, the testability scorer, the generator's
// per-symbol API surface — reads `signature`, `params` and `return_type`. C# emitted none of them,
// so a C# symbol reached the prompt as a name and a line range.
func TestIndexer_signatureCarriesTheJavaContractKeys(t *testing.T) {
	dll := liveDLL(t)
	m, err := Run(context.Background(), minimalFixtureRepo(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	shapes := m["src/Shapes.cs"]
	if shapes == nil {
		t.Fatal("src/Shapes.cs not indexed")
	}
	var add, state *struct {
		Signature  string              `json:"signature"`
		ReturnType string              `json:"return_type"`
		Params     []map[string]string `json:"params"`
		Members    []string            `json:"members"`
		Attributes []string            `json:"attributes"`
		XMLDoc     string              `json:"xmldoc"`
		Exported   bool                `json:"exported"`
	}
	for _, s := range shapes.Symbols {
		if len(s.SignatureJSON) == 0 {
			continue
		}
		var payload struct {
			Signature  string              `json:"signature"`
			ReturnType string              `json:"return_type"`
			Params     []map[string]string `json:"params"`
			Members    []string            `json:"members"`
			Attributes []string            `json:"attributes"`
			XMLDoc     string              `json:"xmldoc"`
			Exported   bool                `json:"exported"`
		}
		if err := json.Unmarshal(s.SignatureJSON, &payload); err != nil {
			continue
		}
		switch s.FQName {
		case "Minimal.Domain.Basket#Add(BasketLine,bool)":
			add = &payload
		case "Minimal.Domain.BasketState":
			state = &payload
		}
	}
	if add == nil {
		t.Fatal("no signature payload for Basket#Add")
	}
	if !strings.Contains(add.Signature, "decimal Add(BasketLine line, bool merge = true)") {
		t.Errorf("signature = %q, want the declaration header with its default", add.Signature)
	}
	if add.ReturnType != "decimal" {
		t.Errorf("return_type = %q, want decimal", add.ReturnType)
	}
	if len(add.Params) != 2 || add.Params[0]["name"] != "line" || add.Params[0]["type"] != "BasketLine" {
		t.Errorf("params = %v, want [{line BasketLine} {merge bool}]", add.Params)
	}
	if len(add.Attributes) != 1 || add.Attributes[0] != "Obsolete" {
		t.Errorf("attributes = %v, want [Obsolete] with the Attribute suffix stripped", add.Attributes)
	}
	if add.XMLDoc != "Adds a line and returns the running total." {
		t.Errorf("xmldoc = %q", add.XMLDoc)
	}
	if !add.Exported {
		t.Error("a public method on a public type is not marked exported")
	}

	if state == nil {
		t.Fatal("no signature payload for BasketState")
	}
	if strings.Join(state.Members, ",") != "Open,Locked,CheckedOut" {
		t.Errorf("enum members = %v, want declaration order with the explicit value dropped", state.Members)
	}
}

// liveDLL returns the locally published indexer DLL, skipping when the live toolchain is absent.
// Roslyn's output is the only thing that can prove what this tool emits, so the test runs the real
// binary or does not run at all.
func liveDLL(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet not on PATH")
	}
	dll, err := filepath.Abs(filepath.Join("publish", "CSharpIndexer.dll"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(dll); statErr != nil {
		t.Skipf("no published DLL at %s (run: dotnet publish -c Release -o publish)", dll)
	}
	return dll
}

func keysOf(m map[string]*indexer.ParsedFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
