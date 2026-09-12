package langid

import "testing"

func TestCanonical(t *testing.T) {
	cases := []struct{ in, want string }{
		{"cs", "csharp"},
		{"CS", "csharp"},
		{"  cs  ", "csharp"},
		{"c#", "csharp"},
		{"C#", "csharp"},
		{"csharp", "csharp"},
		{"CSharp", "csharp"},
		{"dotnet", "csharp"},
		{"js", "javascript"},
		{"JS", "javascript"},
		{"javascript", "javascript"},
		{"ts", "typescript"},
		{"tsx", "typescript"},
		{"jsx", "javascript"},
		{"typescript", "typescript"},
		{"java", "java"},
		{"kotlin", "kotlin"},
		{"", ""},
		{"   ", ""},
		{"rust", "rust"},
	}
	for _, tc := range cases {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIs(t *testing.T) {
	cases := []struct {
		lang, canonical string
		want            bool
	}{
		{"cs", "csharp", true},
		{"C#", "csharp", true},
		{"csharp", "csharp", true},
		{"csharp", "cs", true}, // the right-hand side is normalised too
		{"java", "csharp", false},
		{"ts", "typescript", true},
		{"js", "typescript", false},
		{"", "csharp", false},
		{"csharp", "", false},
	}
	for _, tc := range cases {
		if got := Is(tc.lang, tc.canonical); got != tc.want {
			t.Errorf("Is(%q, %q) = %v, want %v", tc.lang, tc.canonical, got, tc.want)
		}
	}
}

func TestIsCSharp(t *testing.T) {
	for _, in := range []string{"cs", "CS", "c#", "csharp", "CSharp", " cs "} {
		if !IsCSharp(in) {
			t.Errorf("IsCSharp(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "java", "javascript", "csharpish"} {
		if IsCSharp(in) {
			t.Errorf("IsCSharp(%q) = true, want false", in)
		}
	}
}

func TestIsJSTS(t *testing.T) {
	for _, in := range []string{"js", "ts", "jsx", "tsx", "javascript", "typescript", "JS"} {
		if !IsJSTS(in) {
			t.Errorf("IsJSTS(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "java", "csharp", "cs"} {
		if IsJSTS(in) {
			t.Errorf("IsJSTS(%q) = true, want false", in)
		}
	}
}

// Aliases is what a contract test in another package iterates to prove a `case "csharp"` site also
// accepts the short form the runner and the metadata store both emit.
func TestAliasesOf(t *testing.T) {
	got := AliasesOf("csharp")
	want := map[string]bool{"csharp": true, "cs": true, "c#": true, "dotnet": true}
	if len(got) != len(want) {
		t.Fatalf("AliasesOf(csharp) = %v, want %d entries", got, len(want))
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected alias %q", a)
		}
		if Canonical(a) != "csharp" {
			t.Errorf("alias %q does not normalise back to csharp", a)
		}
	}
	if AliasesOf("rust") == nil {
		t.Error("AliasesOf must always include the canonical form itself")
	}
}
