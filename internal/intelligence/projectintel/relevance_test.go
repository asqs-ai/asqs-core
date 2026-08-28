package projectintel

import (
	"math"
	"sort"
	"strings"
	"testing"
)

// ── cosineSimilarity ─────────────────────────────────────────────────────────

func TestCosineSimilarity_ZeroVectors(t *testing.T) {
	if got := cosineSimilarity([]float32{0, 0}, []float32{0, 0}); got != 0 {
		t.Fatalf("want 0, got %v", got)
	}
}

func TestCosineSimilarity_IdenticalVectors(t *testing.T) {
	a := []float32{1, 2, 3}
	got := cosineSimilarity(a, a)
	if math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("want ~1.0, got %v", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := cosineSimilarity(a, b); got != 0 {
		t.Fatalf("want 0, got %v", got)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 2}, []float32{1}); got != 0 {
		t.Fatalf("want 0 for length mismatch, got %v", got)
	}
}

// ── LexicalJaccard ────────────────────────────────────────────────────────────

func TestLexicalJaccard_EmptyQuery(t *testing.T) {
	if got := LexicalJaccard("", "hello world"); got != 0 {
		t.Fatalf("want 0 for empty query, got %v", got)
	}
}

func TestLexicalJaccard_EmptyText(t *testing.T) {
	if got := LexicalJaccard("hello", ""); got != 0 {
		t.Fatalf("want 0 for empty text, got %v", got)
	}
}

func TestLexicalJaccard_ExactMatch(t *testing.T) {
	got := LexicalJaccard("junit test java", "junit test java")
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("want 1.0 for exact match, got %v", got)
	}
}

func TestLexicalJaccard_PartialOverlap(t *testing.T) {
	// "java test" vs "java spring" → intersection={java}, union={java,test,spring}=3 → 1/3
	got := LexicalJaccard("java test", "java spring")
	want := 1.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("want %.4f, got %.4f", want, got)
	}
}

func TestLexicalJaccard_NoOverlap(t *testing.T) {
	got := LexicalJaccard("alpha beta", "gamma delta")
	if got != 0 {
		t.Fatalf("want 0 for no overlap, got %v", got)
	}
}

// ── SkillRelevanceBoost ───────────────────────────────────────────────────────

func TestSkillRelevanceBoost_CappedAt025(t *testing.T) {
	// Provide every keyword to saturate the boost.
	body := "test tests jest vitest playwright cypress junit mockito xunit nunit mstest describe it( documentation jsdoc tsdoc javadoc /// skill description:"
	got := SkillRelevanceBoost("", body)
	if got > 0.25+1e-9 {
		t.Fatalf("boost must not exceed 0.25, got %v", got)
	}
}

func TestSkillRelevanceBoost_ZeroForPlainDoc(t *testing.T) {
	// Body deliberately contains none of the boost keywords.
	got := SkillRelevanceBoost("docs/README.md", "This is a generic readme about configuration and setup.")
	if got != 0 {
		t.Fatalf("want 0 for plain doc, got %v", got)
	}
}

func TestSkillRelevanceBoost_DescriptionPrefix(t *testing.T) {
	got := SkillRelevanceBoost("", "description: something")
	if got <= 0 {
		t.Fatalf("want positive boost for description: keyword, got %v", got)
	}
}

// ── RankByEmbedding ───────────────────────────────────────────────────────────

func TestRankByEmbedding_OrdersByCosineSim(t *testing.T) {
	target := []float32{1, 0} // points toward x-axis

	// payment doc: perfectly aligned with target → sim=1.0
	// auth doc: perpendicular → sim=0.0
	// generic doc: has no embedding
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "auth.md"}, DocEmbedding: []float32{0, 1}},
		{Candidate: Candidate{RelPath: "generic.md"}},
		{Candidate: Candidate{RelPath: "payment.md"}, DocEmbedding: []float32{1, 0}},
	}
	ranked := RankByEmbedding(cands, target)
	if ranked[0].RelPath != "payment.md" {
		t.Fatalf("expected payment.md first (highest cosine sim), got %q", ranked[0].RelPath)
	}
	if ranked[1].RelPath != "auth.md" {
		t.Fatalf("expected auth.md second, got %q", ranked[1].RelPath)
	}
	// generic.md (no embedding) must be last
	if ranked[2].RelPath != "generic.md" {
		t.Fatalf("expected generic.md last (no embedding), got %q", ranked[2].RelPath)
	}
}

func TestRankByEmbedding_EmptyTarget_ReturnsUnchanged(t *testing.T) {
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "a.md"}, DocEmbedding: []float32{1, 0}},
		{Candidate: Candidate{RelPath: "b.md"}, DocEmbedding: []float32{0, 1}},
	}
	ranked := RankByEmbedding(cands, nil)
	// Must preserve original order when target is empty
	if ranked[0].RelPath != "a.md" {
		t.Fatalf("want original order preserved, got %q first", ranked[0].RelPath)
	}
}

func TestRankByEmbedding_NoEmbeddingsUsesPathTieBreak(t *testing.T) {
	// All have no embedding; tie-break by RelPath alphabetically.
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "z.md"}},
		{Candidate: Candidate{RelPath: "a.md"}},
		{Candidate: Candidate{RelPath: "m.md"}},
	}
	ranked := RankByEmbedding(cands, []float32{1, 0})
	paths := []string{ranked[0].RelPath, ranked[1].RelPath, ranked[2].RelPath}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("expected alphabetical tie-break, got %v", paths)
	}
}

// ── SelectForGap ─────────────────────────────────────────────────────────────

func TestSelectForGap_ReturnsTopK(t *testing.T) {
	target := []float32{1, 0}
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "d1.md", Kind: DocKindDoc}, Score: 0.9, Content: "doc1", DocEmbedding: []float32{1, 0}},
		{Candidate: Candidate{RelPath: "d2.md", Kind: DocKindDoc}, Score: 0.5, Content: "doc2", DocEmbedding: []float32{0.9, 0.1}},
		{Candidate: Candidate{RelPath: "d3.md", Kind: DocKindDoc}, Score: 0.3, Content: "doc3", DocEmbedding: []float32{0, 1}},
		{Candidate: Candidate{RelPath: "s1.md", Kind: DocKindSkill}, Score: 0.8, Content: "skill1", DocEmbedding: []float32{1, 0}},
	}
	md := SelectForGap(cands, target, 2, 1, 0, "fallback")
	// Should contain top-2 docs and top-1 skill; d3.md (lowest sim) must be absent
	if !contains(md, "d1.md") {
		t.Fatal("expected d1.md in per-gap markdown")
	}
	if !contains(md, "d2.md") {
		t.Fatal("expected d2.md in per-gap markdown")
	}
	if contains(md, "d3.md") {
		t.Fatal("d3.md should not appear (capped at maxDoc=2)")
	}
	if !contains(md, "s1.md") {
		t.Fatal("expected s1.md in per-gap markdown")
	}
}

func TestSelectForGap_FallsBackWhenNoCandidates(t *testing.T) {
	got := SelectForGap(nil, []float32{1, 0}, 5, 5, 0, "fallback markdown")
	if got != "fallback markdown" {
		t.Fatalf("want fallback markdown, got %q", got)
	}
}

func TestSelectForGap_FallsBackWhenNoEmbedding(t *testing.T) {
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "d.md", Kind: DocKindDoc}, Score: 0.5, Content: "doc"},
	}
	got := SelectForGap(cands, nil, 5, 5, 0, "fallback markdown")
	if got != "fallback markdown" {
		t.Fatalf("want fallback markdown when no target embedding, got %q", got)
	}
}

func TestSelectForGap_MaxTotalRunesTruncates(t *testing.T) {
	longContent := strings.Repeat("a", 500)
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "d.md", Kind: DocKindDoc}, Score: 0.9, Content: longContent, DocEmbedding: []float32{1, 0}},
	}
	// Set a cap that's smaller than the full output to force truncation.
	cap := 200
	got := SelectForGap(cands, []float32{1, 0}, 5, 5, cap, "")
	// Output must be at or under cap + truncation suffix length.
	if len([]rune(got)) > cap+20 {
		t.Fatalf("expected truncated output ≤%d runes, got %d", cap+20, len([]rune(got)))
	}
	// Truncation suffix must be present.
	if !strings.Contains(got, "[truncated]") {
		t.Fatal("expected [truncated] marker in truncated output")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
