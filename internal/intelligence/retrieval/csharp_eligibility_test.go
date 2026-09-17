package retrieval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func symWithSignature(t *testing.T, lang, kind, fq string, sig map[string]interface{}) *metadata.Symbol {
	t.Helper()
	b, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	return &metadata.Symbol{Lang: lang, Kind: kind, FQName: fq, SignatureJSON: b}
}

// The private-member gate read `sym.Lang != "java"` and returned false for everything else. C#
// stores the same `visibility` key — it has since the indexer wrote signatures at all — so a
// private C# method was proposed as a gap, and the generated test could not call it.
func TestIsPrivateMethod_readsVisibilityForEveryLanguageThatStoresIt(t *testing.T) {
	cases := []struct {
		name string
		sym  *metadata.Symbol
		want bool
	}{
		{
			name: "a private C# method",
			sym:  symWithSignature(t, "csharp", "method", "Shop.Basket#Total()", map[string]interface{}{"visibility": "private"}),
			want: true,
		},
		{
			name: "a public C# method",
			sym:  symWithSignature(t, "csharp", "method", "Shop.Basket#Total()", map[string]interface{}{"visibility": "public"}),
			want: false,
		},
		{
			name: "C# internal is not private, but it is not reachable from a test assembly either",
			sym:  symWithSignature(t, "csharp", "method", "Shop.Basket#Total()", map[string]interface{}{"visibility": "internal"}),
			want: false,
		},
		{
			name: "the Java behaviour is unchanged",
			sym:  symWithSignature(t, "java", "method", "Shop.Basket#total()", map[string]interface{}{"visibility": "private"}),
			want: true,
		},
		{
			name: "a language that stores no visibility",
			sym:  symWithSignature(t, "typescript", "FUNCTION", "total", map[string]interface{}{}),
			want: false,
		},
		{
			name: "no signature at all",
			sym:  &metadata.Symbol{Lang: "csharp", Kind: "method", FQName: "Shop.Basket#Total()"},
			want: false,
		},
		{name: "nil", sym: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPrivateMethod(tc.sym); got != tc.want {
				t.Errorf("isPrivateMethod = %v, want %v", got, tc.want)
			}
		})
	}
}

// The accessor filter tested lowercase prefixes against a name that has to start with a capital in
// C#: `strings.HasPrefix("GetTotal", "get")` is false, so every C# property accessor and Get/Is/Has
// method read as a normal method and competed for plan budget with the code worth testing.
func TestIsTrivialAccessorName_csharpCasing(t *testing.T) {
	trivial := []string{
		"Shop.Basket#GetTotal()", "Shop.Basket#SetTotal(int)",
		"Shop.Basket#IsEmpty()", "Shop.Basket#HasLines()",
		// The Java spelling still matches.
		"shop.Basket#getTotal()", "shop.Basket#isEmpty()",
	}
	for _, fq := range trivial {
		if !isTrivialAccessorName(fq) {
			t.Errorf("isTrivialAccessorName(%q) = false, want true", fq)
		}
	}
	// A name that merely starts with those letters is not an accessor: the capital after the prefix
	// is the word boundary, and without it `Setup`, `Issue` and `Hash` would all be excluded.
	notTrivial := []string{
		"Shop.Basket#Setup()", "Shop.Basket#Issue()", "Shop.Basket#Hash()",
		"Shop.Basket#Getaway()", "Shop.Basket#Gettysburg()",
		"Shop.Basket#Total()", "Shop.Basket#Get()",
	}
	for _, fq := range notTrivial {
		if isTrivialAccessorName(fq) {
			t.Errorf("isTrivialAccessorName(%q) = true, want false", fq)
		}
	}
}

// A test cannot name a type its project does not reference. The validation run of 2026-09-12
// discarded four of nine gaps for exactly that — the test project referenced Shop.Core and not
// Shop — and nothing noticed until the compiler said so, after each gap had spent its generation
// and its fix rounds.
func TestCSharpReachableFilter(t *testing.T) {
	repo := csharpSolutionOnDisk(t)

	f := csharpReachableFilter(PlanOptions{Lang: "csharp", RepoPath: repo})
	if len(f.dirs) == 0 {
		t.Fatal("no reachable directories computed for a solution with a test project")
	}
	if !f.allows("src/Shop.Core/Basket.cs") {
		t.Error("a referenced project's source is not reachable")
	}
	if f.allows("src/Shop/Controllers/HomeController.cs") {
		t.Error("a project nothing references is reachable")
	}
}

// Every condition that cannot be established means "no claim". A wrong exclusion silently removes
// work the run should have done, which is worse than a gap that fails to compile.
func TestCSharpReachableFilter_allowsEverythingWithoutEvidence(t *testing.T) {
	repo := csharpSolutionOnDisk(t)
	cases := []struct {
		name string
		opts PlanOptions
	}{
		{"another language", PlanOptions{Lang: "java", RepoPath: repo}},
		{"no repository path", PlanOptions{Lang: "csharp"}},
		{"a repository with no test project", PlanOptions{Lang: "csharp", RepoPath: t.TempDir()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := csharpReachableFilter(tc.opts)
			if !f.allows("src/Shop/Controllers/HomeController.cs") {
				t.Error("filtered a symbol without the evidence to do so")
			}
		})
	}
}

func csharpSolutionOnDisk(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for rel, body := range map[string]string{
		"src/Shop.Core/Shop.Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
		"src/Shop.Core/Basket.cs":        "namespace Shop.Core; public class Basket { }",
		"src/Shop/Shop.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup><ProjectReference Include="..\Shop.Core\Shop.Core.csproj" /></ItemGroup>
</Project>`,
		"src/Shop/Controllers/HomeController.cs": "namespace Shop.Controllers; public class HomeController { }",
		"tests/Shop.Tests/Shop.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="NUnit" Version="4.2.2" /></ItemGroup>
  <ItemGroup><ProjectReference Include="..\..\src\Shop.Core\Shop.Core.csproj" /></ItemGroup>
</Project>`,
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// Arity separates an accessor from a calculation that happens to be named like one. `GetTotal()`
// returns a field; `GetTotal(int qty, decimal unit)` computes something, and excluding it would
// drop exactly the method worth a test.
func TestTrivialAccessorExclusion_respectsArity(t *testing.T) {
	sig := func(params int) []byte {
		if params < 0 {
			return []byte(`{"visibility":"public"}`) // the indexer recorded none
		}
		list := make([]map[string]string, params)
		for i := range list {
			list[i] = map[string]string{"name": "a", "type": "int"}
		}
		b, err := json.Marshal(map[string]interface{}{"visibility": "public", "params": list})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name     string
		params   int
		eligible bool
	}{
		{"a no-argument accessor is trivial", 0, false},
		{"an accessor that takes arguments computes something", 2, true},
		// Not recorded is not evidence of having none: the old behaviour stands.
		{"arity unknown keeps the previous exclusion", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sym := &metadata.Symbol{
				Lang: "csharp", Kind: "method", FQName: "Shop.Basket#GetTotal()",
				StartLine: 10, EndLine: 12, SignatureJSON: sig(tc.params),
			}
			got, reason := gapEligibility(sym, nil, 0)
			if got != tc.eligible {
				t.Errorf("gapEligible = %v (%s), want %v", got, reason, tc.eligible)
			}
		})
	}
}
