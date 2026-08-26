package indexer

import (
	"fmt"
	"strings"
	"testing"
)

// buildLines returns a 1-based line slice (index 0 unused by callers that pass 1-based line numbers)
// shaped like the indexer's own `lines` argument.
func buildLines(bodies []string) []string {
	return bodies
}

// symbolOver returns a symbol spanning the whole fixture.
func symbolOver(lines []string) ParsedSymbol {
	return ParsedSymbol{Kind: "METHOD", FQName: "p.C#m", StartLine: 1, EndLine: len(lines)}
}

// Chunk size must follow the measured content, not a hardcoded characters-per-line guess.
//
// The old rule was `targetLines = MaxTokens*CharsPerToken/80`, i.e. "assume 80 characters per line".
// Minified and generated sources break that assumption in one direction (a single line can be
// thousands of characters, so a fixed line count blows straight through MaxTokens) and heavily
// indented or declaration-dense code breaks it in the other.
func TestSplitLargeSymbol_sizesByMeasuredContent(t *testing.T) {
	cfg := DefaultChunkConfig()
	sanitize := SanitizeOptions{}
	budget := cfg.MaxTokens

	cases := []struct {
		name  string
		lines []string
	}{
		{
			// ~1000 characters per line: 12x the old assumption.
			name: "minified",
			lines: func() []string {
				var out []string
				for i := 0; i < 40; i++ {
					out = append(out, fmt.Sprintf("var a%d=%s;", i, strings.Repeat("1+", 500)))
				}
				return out
			}(),
		},
		{
			// ~8 characters per line: a tenth of the old assumption.
			name: "heavily indented",
			lines: func() []string {
				var out []string
				for i := 0; i < 400; i++ {
					out = append(out, strings.Repeat(" ", 6)+"}")
				}
				return out
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := buildLines(tc.lines)
			parts := splitLargeSymbolToRaw(lines, symbolOver(lines), "definition", cfg, sanitize)
			if len(parts) == 0 {
				t.Fatal("no chunks produced")
			}
			for i, p := range parts {
				got := cfg.ApproxTokens(p.content)
				// One line that alone exceeds the budget is the documented exception: it cannot be
				// split further at line granularity.
				singleLine := p.startLine == p.endLine
				if got > budget && !singleLine {
					t.Errorf("chunk %d (lines %d-%d) is %d tokens, over the %d budget — the split "+
						"is not measuring its content", i, p.startLine, p.endLine, got, budget)
				}
			}
			// And the split must not be so conservative that it wastes the budget: on input this
			// uniform, all but the last chunk should be reasonably full.
			for i, p := range parts[:len(parts)-1] {
				if got := cfg.ApproxTokens(p.content); got < budget/4 {
					t.Errorf("chunk %d is only %d tokens against a %d budget; a fixed line count "+
						"under-fills chunks on short lines", i, got, budget)
				}
			}
		})
	}
}

// Consecutive chunks must overlap, so a construct split across a boundary is visible whole in at
// least one chunk.
func TestSplitLargeSymbol_consecutiveChunksOverlap(t *testing.T) {
	cfg := DefaultChunkConfig()
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, fmt.Sprintf("    doSomething(%d);", i))
	}
	parts := splitLargeSymbolToRaw(lines, symbolOver(lines), "definition", cfg, SanitizeOptions{})
	if len(parts) < 2 {
		t.Fatalf("fixture produced %d chunk(s); the assertion needs at least 2", len(parts))
	}
	for i := 1; i < len(parts); i++ {
		prev, cur := parts[i-1], parts[i]
		if cur.startLine > prev.endLine {
			t.Errorf("chunk %d starts at line %d, after chunk %d ends at %d — no overlap, so a "+
				"construct spanning the boundary is split with neither half self-contained",
				i, cur.startLine, i-1, prev.endLine)
		}
		if cur.startLine <= prev.startLine {
			t.Fatalf("chunk %d starts at %d, not after chunk %d's start %d — the split does not "+
				"advance and would loop", i, cur.startLine, i-1, prev.startLine)
		}
	}
	// Overlap must stay modest: it is duplicated content in the embedding store.
	prev, cur := parts[0], parts[1]
	overlap := prev.endLine - cur.startLine + 1
	span := prev.endLine - prev.startLine + 1
	if overlap*4 > span {
		t.Errorf("overlap is %d of %d lines (>25%%); that is duplicated content in every chunk",
			overlap, span)
	}
}

// A single line larger than the whole budget must still terminate, one chunk per line.
func TestSplitLargeSymbol_oneOversizeLineTerminates(t *testing.T) {
	cfg := DefaultChunkConfig()
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, strings.Repeat("z", cfg.MaxTokens*cfg.CharsPerToken*3))
	}
	parts := splitLargeSymbolToRaw(lines, symbolOver(lines), "definition", cfg, SanitizeOptions{})
	if len(parts) != len(lines) {
		t.Fatalf("got %d chunks for %d oversize lines, want one per line", len(parts), len(lines))
	}
	for i, p := range parts {
		if p.startLine != i+1 || p.endLine != i+1 {
			t.Errorf("chunk %d covers lines %d-%d, want exactly line %d", i, p.startLine, p.endLine, i+1)
		}
	}
}

// Every line of the symbol must appear in at least one chunk. Overlap makes it tempting to advance
// by the wrong amount and skip a line, which would silently drop code from the index.
func TestSplitLargeSymbol_coversEveryLine(t *testing.T) {
	cfg := DefaultChunkConfig()
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fmt.Sprintf("line%d();", i))
	}
	parts := splitLargeSymbolToRaw(lines, symbolOver(lines), "definition", cfg, SanitizeOptions{})
	covered := make([]bool, len(lines)+1)
	for _, p := range parts {
		for l := p.startLine; l <= p.endLine; l++ {
			if l >= 1 && l < len(covered) {
				covered[l] = true
			}
		}
	}
	for l := 1; l <= len(lines); l++ {
		if !covered[l] {
			t.Fatalf("line %d appears in no chunk; the split skipped it", l)
		}
	}
}
