package retrieval

import (
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// TestFQNameIsBindingPattern_rejectsPatterns is the regression for the run of 2026-09-01, whose
// unit gap 20 was `src.pages.OrdersPage.{ rows, summary }` — the destructuring at
// OrdersPage.tsx:13, not a symbol. It cost a generation call and its artifact was merged into the
// real OrdersPage test, taking that file's compile with it.
func TestFQNameIsBindingPattern_rejectsPatterns(t *testing.T) {
	for _, fq := range []string{
		"src.pages.OrdersPage.{ rows, summary }",
		"src.pages.OrdersPage.{rows,summary}",
		"src.features.orders.useOrders.{ a = x.y }",
		"src.pages.Counter.[count, setCount]",
		"src.pages.Counter.[count]",
	} {
		if !fqNameIsBindingPattern(fq) {
			t.Errorf("fqNameIsBindingPattern(%q) = false, want true", fq)
		}
	}
}

// The far more important direction: a false positive silently drops a real gap, so every FQName
// format this project stores must survive. C# shapes are taken from the format block in
// tools/csharp-indexer/Program.cs; Java from JavaIndexer's classFq + "#" + name.
func TestFQNameIsBindingPattern_keepsRealSymbols(t *testing.T) {
	for _, fq := range []string{
		// TypeScript / JavaScript
		"src.lib.validation.parsePositiveInt",
		"src.app.router.announcementRoutes",
		"src.pages.OrdersPage.OrdersPage",
		// Java: parameterless, '#'-separated members
		"com.example.owner.OwnerController#showOwner",
		"com.example.owner.OwnerRepository",
		// C#: generics on the type, parameter list ALWAYS present on methods
		"Example.Api.OrderService#Create(int,string)",
		"Example.Api.Repo<T>#Add<TItem>(TItem)",
		"Example.Api.Repo<T>#Items",
		"Example.Api.Order#.ctor(string)",
		// C# array and tuple parameters: '[' appears, but never opening a segment
		"Example.Api.Batch#Run(int[],string)",
		"Example.Api.Batch#Pair((int, string))",
		"Example.Api.Batch#Nested(List<int[]>)",
	} {
		if fqNameIsBindingPattern(fq) {
			t.Errorf("fqNameIsBindingPattern(%q) = true, want false — this drops a real gap", fq)
		}
	}
}

func TestFQNameIsBindingPattern_emptyIsNotAPattern(t *testing.T) {
	if fqNameIsBindingPattern("") || fqNameIsBindingPattern("   ") {
		t.Error("an empty FQName must not be classified as a binding pattern")
	}
}

// gapEligibility must report the pattern reason ahead of any span-derived one, so
// plan.gaps_filtered_ineligible attributes the drop to the right cause.
func TestGapEligibility_rejectsBindingPatternBeforeSpanRules(t *testing.T) {
	sym := &metadata.Symbol{
		FQName:    "src.pages.OrdersPage.{ rows, summary }",
		Kind:      "VARIABLE",
		StartLine: 13,
		EndLine:   20, // a span wide enough that no other rule would fire
	}
	ok, reason := gapEligibility(sym, nil, 3)
	if ok {
		t.Fatal("a destructuring pattern must not be an eligible gap")
	}
	if reason != IneligibleBindingPattern {
		t.Errorf("reason = %q, want %q", reason, IneligibleBindingPattern)
	}
}

// And a real variable with the same shape of metadata stays eligible.
func TestGapEligibility_keepsRealVariable(t *testing.T) {
	sym := &metadata.Symbol{
		FQName:    "src.app.router.announcementRoutes",
		Kind:      "VARIABLE",
		StartLine: 19,
		EndLine:   26,
	}
	if ok, reason := gapEligibility(sym, nil, 3); !ok {
		t.Errorf("a real variable was dropped as %q", reason)
	}
}
