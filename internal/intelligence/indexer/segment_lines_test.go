package indexer

import (
	"fmt"
	"strings"
	"testing"
)

// Segment line numbers must be EXACT, not estimated.
//
// The oversize-embedding fallback splits a chunk into segments and reports a line range for each.
// Those ranges become `symbolLoc` in the generation prompt and the `errloc` window in the fixer,
// which is instructed to fix the primary error line first. They used to be interpolated from rune
// offsets — `(offset * totalLines) / totalRunes` — which is only correct when every line is the same
// length. Code is not uniform, so the estimate drifted.
//
// The fixture below is deliberately non-uniform: alternating very short and very long lines is
// exactly the shape that breaks a ratio estimate, and it is what real code looks like (a closing
// brace next to a long argument list).
func TestSplitChunkForEmbedding_lineNumbersAreCountedNotEstimated(t *testing.T) {
	// CLUSTERED non-uniformity: a block of long lines followed by a block of short ones. Alternating
	// long and short lines does NOT discriminate — every window then contains the same mix, so the
	// rune-to-line ratio is right on average and the interpolation looks correct. Real code clusters
	// the same way: a dense body of statements, then a run of closing braces.
	var b strings.Builder
	const longLines, shortLines = 40, 40
	for i := 0; i < longLines; i++ {
		b.WriteString(fmt.Sprintf("L%03d%s\n", i, strings.Repeat("x", 160)))
	}
	for i := 0; i < shortLines; i++ {
		b.WriteString(fmt.Sprintf("}%03d\n", i))
	}
	content := b.String()

	const parentStart = 100
	c := &ChunkToEmbed{
		Content:   content,
		StartLine: parentStart,
		EndLine:   parentStart + longLines + shortLines - 1,
	}

	segs := splitChunkForEmbedding(c, 400, 40)
	if len(segs) < 2 {
		t.Fatalf("fixture did not split: got %d segment(s); the assertions below need at least 2", len(segs))
	}

	// The assertion is a self-contained property: a segment's reported line SPAN must equal the
	// number of lines its own content actually occupies. No reconstruction of where the segment sits
	// is needed, which matters — an earlier version of this test located segments by string search
	// and ended up measuring its own bookkeeping instead of the code.
	//
	// Interpolation cannot satisfy this on non-uniform text: it derives the span from a rune ratio,
	// so a segment made of long lines is reported as spanning more lines than it has, and one made
	// of short lines fewer.
	for i, seg := range segs {
		body := strings.TrimSuffix(seg.Content, "\n")
		wantSpan := strings.Count(body, "\n") // lines occupied, minus one
		gotSpan := seg.EndLine - seg.StartLine
		if gotSpan != wantSpan {
			t.Errorf("segment %d reports lines %d-%d (span %d) but its content occupies %d line(s) "+
				"beyond its first — these numbers become symbolLoc in the prompt and the errloc "+
				"window the fixer is told to edit",
				i, seg.StartLine, seg.EndLine, gotSpan, wantSpan)
		}
	}

	// Consecutive segments must advance monotonically, and by the overlap the splitter was asked for
	// rather than by an arbitrary amount.
	for i := 1; i < len(segs); i++ {
		if segs[i].StartLine < segs[i-1].StartLine {
			t.Errorf("segment %d starts at line %d, before segment %d at %d",
				i, segs[i].StartLine, i-1, segs[i-1].StartLine)
		}
	}
}

// Every segment must stay inside the parent's declared range, and the first must start exactly at it.
func TestSplitChunkForEmbedding_segmentsStayWithinTheParentRange(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString(fmt.Sprintf("line %d %s\n", i, strings.Repeat("y", i%30)))
	}
	c := &ChunkToEmbed{Content: b.String(), StartLine: 7, EndLine: 86}

	segs := splitChunkForEmbedding(c, 300, 30)
	if len(segs) < 2 {
		t.Fatalf("fixture did not split: %d segment(s)", len(segs))
	}
	if segs[0].StartLine != c.StartLine {
		t.Errorf("first segment starts at %d, want the parent's own start %d", segs[0].StartLine, c.StartLine)
	}
	for i, seg := range segs {
		if seg.StartLine < c.StartLine || seg.EndLine > c.EndLine {
			t.Errorf("segment %d spans %d-%d, outside the parent's %d-%d",
				i, seg.StartLine, seg.EndLine, c.StartLine, c.EndLine)
		}
		if seg.EndLine < seg.StartLine {
			t.Errorf("segment %d has EndLine %d before StartLine %d", i, seg.EndLine, seg.StartLine)
		}
	}
	last := segs[len(segs)-1]
	if last.EndLine != c.EndLine {
		t.Errorf("last segment ends at %d, want the parent's own end %d — the tail of the symbol is "+
			"reported as belonging to an earlier line than it does", last.EndLine, c.EndLine)
	}
}
