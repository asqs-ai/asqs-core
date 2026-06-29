package projectintel

import (
	"testing"
)

func TestLexicalJaccard_EmptyQuery(t *testing.T) {
	if s := LexicalJaccard("", "some text here"); s != 0 {
		t.Fatalf("expected 0, got %f", s)
	}
}

func TestLexicalJaccard_ExactMatch(t *testing.T) {
	if s := LexicalJaccard("hello world", "hello world"); s != 1.0 {
		t.Fatalf("expected 1.0, got %f", s)
	}
}

func TestLexicalJaccard_PartialOverlap(t *testing.T) {
	s := LexicalJaccard("java test junit", "java spring boot")
	if s <= 0 || s >= 1 {
		t.Fatalf("expected fractional score, got %f", s)
	}
}

func TestLexicalJaccard_NoOverlap(t *testing.T) {
	if s := LexicalJaccard("abc def", "xyz uvw"); s != 0 {
		t.Fatalf("expected 0, got %f", s)
	}
}

func TestSkillRelevanceBoost_CappedAt025(t *testing.T) {
	body := "test tests jest vitest playwright cypress junit mockito xunit nunit mstest describe it( documentation jsdoc tsdoc javadoc /// skill description:"
	score := SkillRelevanceBoost("SKILL.md", body)
	if score > 0.25 {
		t.Fatalf("boost must not exceed 0.25, got %f", score)
	}
}

func TestSkillRelevanceBoost_ZeroForPlainDoc(t *testing.T) {
	score := SkillRelevanceBoost("README.md", "This is a generic readme about configuration and setup.")
	if score != 0 {
		t.Fatalf("expected 0 for plain doc, got %f", score)
	}
}

func TestSkillRelevanceBoost_PositiveForTestKeyword(t *testing.T) {
	score := SkillRelevanceBoost("SKILL.md", "Write unit tests using junit and mockito.")
	if score <= 0 {
		t.Fatalf("expected positive boost, got %f", score)
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{0.5, 0.5, 0.5}
	if s := cosineSimilarity(v, v); s < 0.999 {
		t.Fatalf("expected ~1.0, got %f", s)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if s := cosineSimilarity(a, b); s != 0 {
		t.Fatalf("expected 0 for orthogonal, got %f", s)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1}
	if s := cosineSimilarity(a, b); s != 0 {
		t.Fatalf("expected 0 for length mismatch, got %f", s)
	}
}

func TestRankByEmbedding_OrdersByCosineSim(t *testing.T) {
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "low.md"}, DocEmbedding: []float32{0, 1}},
		{Candidate: Candidate{RelPath: "high.md"}, DocEmbedding: []float32{1, 0}},
	}
	target := []float32{1, 0}
	ranked := RankByEmbedding(cands, target)
	if ranked[0].RelPath != "high.md" {
		t.Fatalf("expected high.md first, got %s", ranked[0].RelPath)
	}
}

func TestRankByEmbedding_NoEmbeddingFallsToBottom(t *testing.T) {
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "no-embed.md"}},
		{Candidate: Candidate{RelPath: "has-embed.md"}, DocEmbedding: []float32{1, 0}},
	}
	target := []float32{1, 0}
	ranked := RankByEmbedding(cands, target)
	if ranked[0].RelPath != "has-embed.md" {
		t.Fatalf("candidate with embedding should rank first")
	}
}

func TestRankByEmbedding_EmptyTarget(t *testing.T) {
	cands := []RankedCandidate{{Candidate: Candidate{RelPath: "a.md"}}}
	ranked := RankByEmbedding(cands, nil)
	if len(ranked) != 1 || ranked[0].RelPath != "a.md" {
		t.Fatal("empty target should return candidates unchanged")
	}
}

func TestSelectForGap_ReturnsFallbackWhenNoCandidates(t *testing.T) {
	got := SelectForGap(nil, []float32{1, 0}, 5, 5, 0, "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestSelectForGap_ReturnsFallbackWhenNoEmbedding(t *testing.T) {
	cands := []RankedCandidate{{Candidate: Candidate{RelPath: "a.md"}, Content: "hello"}}
	got := SelectForGap(cands, nil, 5, 5, 0, "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestSelectForGap_ReturnsTopK(t *testing.T) {
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "a.md", Kind: DocKindDoc}, DocEmbedding: []float32{1, 0}, Content: "A"},
		{Candidate: Candidate{RelPath: "b.md", Kind: DocKindDoc}, DocEmbedding: []float32{0.5, 0.5}, Content: "B"},
		{Candidate: Candidate{RelPath: "c.md", Kind: DocKindDoc}, DocEmbedding: []float32{0, 1}, Content: "C"},
	}
	got := SelectForGap(cands, []float32{1, 0}, 2, 2, 0, "fallback")
	if got == "fallback" {
		t.Fatal("expected non-fallback")
	}
	// a.md should appear (highest cosine sim), c.md should not
	if !containsStr(got, "a.md") {
		t.Fatalf("expected a.md in output, got:\n%s", got)
	}
}

func TestSelectForGap_MaxTotalRunesTruncates(t *testing.T) {
	body := make([]byte, 500)
	for i := range body {
		body[i] = 'x'
	}
	cands := []RankedCandidate{
		{Candidate: Candidate{RelPath: "big.md", Kind: DocKindDoc}, DocEmbedding: []float32{1}, Content: string(body)},
	}
	got := SelectForGap(cands, []float32{1}, 5, 5, 200, "fallback")
	if !containsStr(got, "[truncated]") {
		t.Fatalf("expected truncation marker, got:\n%s", got)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
