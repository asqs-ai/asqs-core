package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// bigChunk builds a search result whose body is n characters of recognisable filler.
func bigChunk(file string, n int) embeddings.SearchResult {
	return embeddings.SearchResult{Chunk: embeddings.Chunk{
		File: file, StartLine: 1, EndLine: 200, ChunkType: "definition",
		Content: strings.Repeat("x", n),
	}}
}

// The regression: full chunk bodies were concatenated and the registry cap cut whatever came last,
// so a k=5 search over average Java chunks reached 6000 chars inside the first two or three results
// and the remainder vanished mid-list with no indication they existed.
func TestSearchCode_everyResultSurvivesTheCap(t *testing.T) {
	r, _, c := testRegistry(t)
	for i := 0; i < 5; i++ {
		c.lexical = append(c.lexical, bigChunk(fmt.Sprintf("Big%d.java", i), 4000))
	}

	out := invoke(t, r, ToolSearchCode, `{"query":"x","k":5}`)

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("Big%d.java", i)
		if !strings.Contains(out, name) {
			t.Errorf("result %s was dropped entirely:\n%s", name, out[:min(len(out), 400)])
		}
	}
	if len(out) > DefaultMaxChars {
		t.Errorf("output is %d chars, over the %d cap", len(out), DefaultMaxChars)
	}
}

// Each body must carry real content, not just a header followed by a truncation marker.
func TestSearchCode_eachResultKeepsReadableBody(t *testing.T) {
	r, _, c := testRegistry(t)
	for i := 0; i < 5; i++ {
		c.lexical = append(c.lexical, bigChunk(fmt.Sprintf("Big%d.java", i), 4000))
	}

	out := invoke(t, r, ToolSearchCode, `{"query":"x","k":5}`)

	for _, seg := range strings.Split(out, "\n--- ")[1:] {
		body := seg
		if i := strings.Index(seg, "\n"); i >= 0 {
			body = seg[i+1:]
		}
		if n := strings.Count(body, "x"); n < 200 {
			t.Errorf("a result kept only %d body chars; snippets this short are not readable:\n%s", n, seg[:min(len(seg), 200)])
		}
	}
}

// A short result must not be padded with an allowance it cannot use; the slack belongs to the
// long results that actually need it.
func TestSearchCode_shortResultsDonateTheirSlack(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{
		{Chunk: embeddings.Chunk{File: "Tiny.java", StartLine: 1, EndLine: 2, ChunkType: "definition", Content: "class Tiny {}"}},
		bigChunk("Huge.java", 9000),
	}

	out := invoke(t, r, ToolSearchCode, `{"query":"x","k":2}`)

	if !strings.Contains(out, "class Tiny {}") {
		t.Errorf("the short result should be shown whole:\n%s", out)
	}
	// An even split would have given Huge.java about half the budget. Fair sharing hands it
	// everything Tiny.java did not use.
	if n := strings.Count(out, "x"); n < DefaultMaxChars/2 {
		t.Errorf("long result kept %d chars; it should have absorbed the short one's slack", n)
	}
}

// capped() reports only its own cut. A result trimmed by the per-result budget must still set the
// flag, or a drilldown shows a complete result where half the body is missing.
func TestSearchCode_perResultTrimSetsTruncatedFlag(t *testing.T) {
	r, _, c := testRegistry(t)
	for i := 0; i < 4; i++ {
		c.lexical = append(c.lexical, bigChunk(fmt.Sprintf("Big%d.java", i), 4000))
	}

	invoke(t, r, ToolSearchCode, `{"query":"x","k":4}`)

	if !r.LastResultTruncated() {
		t.Fatal("bodies were trimmed but LastResultTruncated() reports false")
	}
}

func TestSearchCode_smallResultsAreUntouched(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{
		{Chunk: embeddings.Chunk{File: "A.java", StartLine: 1, EndLine: 2, ChunkType: "definition", Content: "class A {}"}},
		{Chunk: embeddings.Chunk{File: "B.java", StartLine: 1, EndLine: 2, ChunkType: "definition", Content: "class B {}"}},
	}

	out := invoke(t, r, ToolSearchCode, `{"query":"x","k":2}`)

	if strings.Contains(out, "truncated") {
		t.Errorf("results that fit must not be marked truncated:\n%s", out)
	}
	if r.LastResultTruncated() {
		t.Error("LastResultTruncated() true for results that all fit")
	}
}

func TestShareBudget(t *testing.T) {
	t.Run("equal items split evenly", func(t *testing.T) {
		got := shareBudget([]string{strings.Repeat("a", 100), strings.Repeat("b", 100)}, 60)
		if got[0] != 30 || got[1] != 30 {
			t.Errorf("got %v, want [30 30]", got)
		}
	})
	t.Run("short item donates the remainder", func(t *testing.T) {
		got := shareBudget([]string{"abc", strings.Repeat("b", 100)}, 60)
		if got[0] != 3 || got[1] != 57 {
			t.Errorf("got %v, want [3 57]", got)
		}
	})
	t.Run("everything fits", func(t *testing.T) {
		got := shareBudget([]string{"abc", "de"}, 60)
		if got[0] != 3 || got[1] != 2 {
			t.Errorf("got %v, want [3 2]", got)
		}
	})
	t.Run("no budget", func(t *testing.T) {
		got := shareBudget([]string{"abc"}, 0)
		if got[0] != 0 {
			t.Errorf("got %v, want [0]", got)
		}
	})
	t.Run("never exceeds the total", func(t *testing.T) {
		items := []string{strings.Repeat("a", 500), "bb", strings.Repeat("c", 90), strings.Repeat("d", 7)}
		sum := 0
		for _, n := range shareBudget(items, 200) {
			sum += n
		}
		if sum > 200 {
			t.Errorf("allocated %d over a total of 200", sum)
		}
	})
}

func TestFitWithin(t *testing.T) {
	t.Run("fits untouched", func(t *testing.T) {
		got, cut := fitWithin("abc", 10)
		if got != "abc" || cut {
			t.Errorf("got %q cut=%v", got, cut)
		}
	})
	t.Run("marker is paid for out of the budget", func(t *testing.T) {
		s := strings.Repeat("x", 500)
		got, cut := fitWithin(s, 120)
		if !cut {
			t.Fatal("want cut=true")
		}
		if len(got) > 120 {
			t.Errorf("returned %d bytes for a budget of 120: %q", len(got), got)
		}
		if !strings.Contains(got, "truncated") {
			t.Errorf("no truncation marker: %q", got)
		}
	})
	t.Run("budget too small for content and marker", func(t *testing.T) {
		got, cut := fitWithin(strings.Repeat("x", 500), 10)
		if got != "" || !cut {
			t.Errorf("got %q cut=%v; want an empty body rather than a bare marker", got, cut)
		}
	})
	t.Run("never splits a rune", func(t *testing.T) {
		// Multi-byte throughout, so a naive byte cut lands mid-rune at most budgets.
		s := strings.Repeat("日本語", 400)
		for budget := 60; budget < 200; budget++ {
			got, _ := fitWithin(s, budget)
			if !utf8.ValidString(got) {
				t.Fatalf("budget %d produced invalid UTF-8: %q", budget, got)
			}
			if len(got) > budget {
				t.Fatalf("budget %d returned %d bytes", budget, len(got))
			}
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
