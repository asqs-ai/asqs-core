package retrieval

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func gap(priority int, fq, file string, start int, id string) *TestGap {
	return &TestGap{
		Priority: priority,
		Symbol:   &metadata.Symbol{ID: id, FQName: fq, File: file, StartLine: start},
	}
}

// TestSortByPriority_deterministicAcrossSymbolIDChurn is the regression test for the half of H-7
// that is not about speed.
//
// The tie-break used to be Symbol.ID, a UUID that InsertSymbol regenerates on every reindex. Two
// consecutive runs on an unchanged repo could therefore order equal-priority gaps differently and
// select disjoint gap sets — which breaks the "incrementally improves the codebase" story and makes
// the config-revision A/B comparison meaningless, because A and B would be testing different gaps.
func TestSortByPriority_deterministicAcrossSymbolIDChurn(t *testing.T) {
	build := func(idSuffix string) []*TestGap {
		return []*TestGap{
			gap(10, "com.acme.B#m", "B.java", 5, "id-b-"+idSuffix),
			gap(10, "com.acme.A#m", "A.java", 9, "id-a-"+idSuffix),
			gap(10, "com.acme.C#m", "C.java", 1, "id-c-"+idSuffix),
			gap(20, "com.acme.Z#m", "Z.java", 3, "id-z-"+idSuffix),
		}
	}
	// Same symbols, completely different UUIDs — as after a reindex.
	first := sortByPriority(build("run1"))
	second := sortByPriority(build("run2"))

	if len(first) != len(second) {
		t.Fatalf("length mismatch")
	}
	for i := range first {
		if first[i].Symbol.FQName != second[i].Symbol.FQName {
			t.Fatalf("order differs at %d after symbol-id churn: %q vs %q",
				i, first[i].Symbol.FQName, second[i].Symbol.FQName)
		}
	}
	// Highest priority first, then FQName ascending.
	want := []string{"com.acme.Z#m", "com.acme.A#m", "com.acme.B#m", "com.acme.C#m"}
	for i, w := range want {
		if first[i].Symbol.FQName != w {
			t.Errorf("position %d = %q, want %q", i, first[i].Symbol.FQName, w)
		}
	}
}

// Input order must not affect the result either — the planner builds this list from a query whose
// row order is not guaranteed.
func TestSortByPriority_independentOfInputOrder(t *testing.T) {
	base := []*TestGap{
		gap(5, "a.A#m", "A.java", 1, "1"),
		gap(5, "a.B#m", "B.java", 1, "2"),
		gap(9, "a.C#m", "C.java", 1, "3"),
		gap(1, "a.D#m", "D.java", 1, "4"),
		gap(5, "a.E#m", "E.java", 1, "5"),
	}
	fqOrder := func(list []*TestGap) []string {
		out := make([]string, len(list))
		for i, g := range list {
			out[i] = g.Symbol.FQName
		}
		return out
	}

	shuffled := append([]*TestGap(nil), base...)
	want := fqOrder(sortByPriority(append([]*TestGap(nil), base...)))

	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 20; trial++ {
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := fqOrder(sortByPriority(append([]*TestGap(nil), shuffled...)))
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("trial %d: order depends on input order: got %v, want %v", trial, got, want)
			}
		}
	}
}

// Same FQName in different files (an overload split across partial classes, or the same simple name
// in two modules) still needs a total order.
func TestSortByPriority_tieBreaksBeyondFQName(t *testing.T) {
	list := sortByPriority([]*TestGap{
		gap(1, "a.A#m", "second.java", 10, "x"),
		gap(1, "a.A#m", "first.java", 99, "y"),
		gap(1, "a.A#m", "first.java", 5, "z"),
	})
	if list[0].Symbol.File != "first.java" || list[0].Symbol.StartLine != 5 {
		t.Errorf("first = %s:%d, want first.java:5", list[0].Symbol.File, list[0].Symbol.StartLine)
	}
	if list[1].Symbol.File != "first.java" || list[1].Symbol.StartLine != 99 {
		t.Errorf("second = %s:%d, want first.java:99", list[1].Symbol.File, list[1].Symbol.StartLine)
	}
}

func TestSortByPriority_nilSymbolsDoNotPanic(t *testing.T) {
	list := sortByPriority([]*TestGap{
		{Priority: 1},
		gap(1, "a.A#m", "A.java", 1, "x"),
		{Priority: 1},
	})
	if len(list) != 3 {
		t.Fatalf("length changed: %d", len(list))
	}
	// Deterministic even with nils: run it again and compare.
	again := sortByPriority([]*TestGap{
		{Priority: 1},
		gap(1, "a.A#m", "A.java", 1, "x"),
		{Priority: 1},
	})
	for i := range list {
		a, b := list[i].Symbol, again[i].Symbol
		if (a == nil) != (b == nil) {
			t.Fatalf("nil placement is not deterministic at %d", i)
		}
	}
}

func TestSortByPriority_largeInputIsSorted(t *testing.T) {
	// The old implementation was an insertion sort, i.e. O(n^2) on unsorted input; at 30k symbols
	// that is ~4.5e8 comparisons on the planning hot path for a list truncated to max_gaps.
	const n = 20000
	rng := rand.New(rand.NewSource(7))
	list := make([]*TestGap, n)
	for i := range list {
		list[i] = gap(rng.Intn(50), fmt.Sprintf("pkg.C%06d#m", i), fmt.Sprintf("F%06d.java", i), i, fmt.Sprint(i))
	}
	sorted := sortByPriority(list)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1].Priority < sorted[i].Priority {
			t.Fatalf("not sorted by priority at %d: %d then %d", i, sorted[i-1].Priority, sorted[i].Priority)
		}
	}
}
