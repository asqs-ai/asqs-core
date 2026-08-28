package apisurface

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const csRepo = "testdata/cs_repo"

// csProviderIsolated returns a provider whose global-packages fallback points at an empty
// directory, so a test can never accidentally resolve against this machine's real NuGet cache.
func csProviderIsolated(t *testing.T) *CSharpProvider {
	t.Helper()
	p := NewCSharpProvider()
	p.nugetRoot = t.TempDir()
	return p
}

func TestCSharpProvider_resolvesAssertionInterfaces(t *testing.T) {
	p := csProviderIsolated(t)
	got, err := p.Lookup(context.Background(), csRepo, []Target{
		{Kind: KindType, Name: "Microsoft.Playwright.IAPIResponseAssertions"},
		{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
		{Kind: KindType, Name: "Microsoft.Playwright.IPageAssertions"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resolved %d surfaces, want 3: %+v", len(got), got)
	}

	api := surfaceFor(t, got, "Microsoft.Playwright.IAPIResponseAssertions")
	if len(api.Members) != 2 {
		t.Errorf("IAPIResponseAssertions has %d members, want 2 (ToBeOKAsync, Not): %v", len(api.Members), api.Members)
	}
	if !memberNamed(api.Members, "ToBeOKAsync") || !memberNamed(api.Members, "Not") {
		t.Errorf("members = %v", api.Members)
	}
	if api.Origin != "Microsoft.Playwright.xml" {
		t.Errorf("origin = %q", api.Origin)
	}
}

// A T: entry describes the type, not a member. Emitting it would present the type's own name as a
// callable member.
func TestCSharpProvider_skipsTypeEntries(t *testing.T) {
	p := csProviderIsolated(t)
	got, _ := p.Lookup(context.Background(), csRepo, []Target{
		{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
	})
	loc := surfaceFor(t, got, "Microsoft.Playwright.ILocatorAssertions")
	for _, m := range loc.Members {
		if strings.HasPrefix(m, "ILocatorAssertions") {
			t.Errorf("a T: entry leaked in as a member: %q", m)
		}
	}
}

func TestCSharpProvider_keepsOverloads(t *testing.T) {
	p := csProviderIsolated(t)
	got, _ := p.Lookup(context.Background(), csRepo, []Target{
		{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
	})
	loc := surfaceFor(t, got, "Microsoft.Playwright.ILocatorAssertions")
	n := 0
	for _, m := range loc.Members {
		if memberName(m) == "ToContainTextAsync" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("kept %d ToContainTextAsync overloads, want 2: %v", n, loc.Members)
	}
}

// TestParseDocMemberID pins the doc-ID decoding directly, including the shapes that are easy to
// get wrong: nested generics (whose commas must not split the parameter list), arrays, by-ref, and
// non-member entry kinds.
func TestParseDocMemberID(t *testing.T) {
	cases := []struct {
		id       string
		wantType string
		wantDecl string
		wantOK   bool
	}{
		{
			id:       "M:Microsoft.Playwright.ILocatorAssertions.ToContainTextAsync(System.String,Microsoft.Playwright.LocatorAssertionsToContainTextOptions)",
			wantType: "Microsoft.Playwright.ILocatorAssertions",
			wantDecl: "ToContainTextAsync(string, LocatorAssertionsToContainTextOptions);",
			wantOK:   true,
		},
		{
			id:       "M:A.B.C.Generic(System.Collections.Generic.IEnumerable{System.String})",
			wantType: "A.B.C",
			wantDecl: "Generic(IEnumerable<string>);",
			wantOK:   true,
		},
		{
			// The comma inside the dictionary's argument list must not split the parameter list.
			id:       "M:A.B.C.Nested(System.Collections.Generic.IDictionary{System.String,System.Collections.Generic.IEnumerable{System.Int32}})",
			wantType: "A.B.C",
			wantDecl: "Nested(IDictionary<string, IEnumerable<int>>);",
			wantOK:   true,
		},
		{
			id:       "M:A.B.C.ArrayAndByRef(System.String[],System.Int32@)",
			wantType: "A.B.C",
			wantDecl: "ArrayAndByRef(string[], int&);",
			wantOK:   true,
		},
		{
			id:       "M:A.B.C.Arity(System.Func{System.Int32,System.Boolean})",
			wantType: "A.B.C",
			wantDecl: "Arity(Func<int, bool>);",
			wantOK:   true,
		},
		{id: "M:A.B.C.NoArgs", wantType: "A.B.C", wantDecl: "NoArgs();", wantOK: true},
		{id: "P:A.B.C.Not", wantType: "A.B.C", wantDecl: "Not;", wantOK: true},
		{id: "F:A.B.C.Field", wantType: "A.B.C", wantDecl: "Field;", wantOK: true},
		{id: "T:A.B.C", wantOK: false},
		{id: "N:A.B", wantOK: false},
		{id: "", wantOK: false},
		{id: "garbage", wantOK: false},
		{id: "M:NoDots", wantOK: false},
		{id: "M:A.B.C.Unbalanced(System.String", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			gotType, gotDecl, ok := parseDocMemberID(tc.id)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if gotType != tc.wantType || gotDecl != tc.wantDecl {
				t.Errorf("got (%q, %q), want (%q, %q)", gotType, gotDecl, tc.wantType, tc.wantDecl)
			}
		})
	}
}

// Generic arity ticks (IEnumerable`1) are noise once the argument list is rendered.
func TestShortenDocType_stripsArityTick(t *testing.T) {
	if got := shortenDocType("System.Collections.Generic.IEnumerable`1{System.String}"); got != "IEnumerable<string>" {
		t.Errorf("got %q, want IEnumerable<string>", got)
	}
}

func TestCSharpProvider_missesAndErrors(t *testing.T) {
	p := csProviderIsolated(t)

	t.Run("unknown type is a miss, not an error", func(t *testing.T) {
		got, err := p.Lookup(context.Background(), csRepo, []Target{
			{Kind: KindType, Name: "Microsoft.Playwright.INoSuchAssertions"},
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d surfaces, want 0", len(got))
		}
	})

	t.Run("no docs anywhere is an actionable error", func(t *testing.T) {
		q := csProviderIsolated(t)
		if _, err := q.Lookup(context.Background(), t.TempDir(), []Target{
			{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
		}); err == nil {
			t.Error("expected an error naming where it searched")
		}
	})

	t.Run("empty inputs are a no-op", func(t *testing.T) {
		if got, err := p.Lookup(context.Background(), "", []Target{{Kind: KindType, Name: "X"}}); err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
		if got, err := p.Lookup(context.Background(), csRepo, nil); err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
	})
}

// Unrelated XML in the tree must not be read as documentation: the candidate filter matches doc
// files against the targets' namespaces.
func TestCSharpProvider_ignoresUnrelatedXML(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/bin/Release/net8.0/App.config.xml", `<?xml version="1.0"?><configuration/>`)
	p := csProviderIsolated(t)
	if _, err := p.Lookup(context.Background(), dir, []Target{
		{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
	}); err == nil {
		t.Error("an unrelated XML file should not count as documentation")
	}
}

func TestCSharpProvider_honoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := csProviderIsolated(t)
	if _, err := p.Lookup(ctx, csRepo, []Target{
		{Kind: KindType, Name: "Microsoft.Playwright.ILocatorAssertions"},
	}); err == nil {
		t.Error("expected the cancelled context to be reported")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
