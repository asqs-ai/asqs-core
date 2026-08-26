package retrieval

import (
	"context"
	"math"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

func sr(id, file string, line int, dist float64, emb []float32) embeddings.SearchResult {
	return embeddings.SearchResult{
		Chunk:    embeddings.Chunk{ID: id, File: file, StartLine: line, ChunkType: "test", Embedding: emb},
		Distance: dist,
	}
}

// FuseRRF against a hand-computed example, including the case the widening path used to create:
// the same document twice in ONE list.
func TestFuseRRF_handComputedAndDuplicateSafe(t *testing.T) {
	// list A: a, b   list B: b, a
	listA := []embeddings.SearchResult{sr("a", "A.java", 1, 0, nil), sr("b", "B.java", 1, 0, nil)}
	listB := []embeddings.SearchResult{sr("b", "B.java", 1, 0, nil), sr("a", "A.java", 1, 0, nil)}

	got := FuseRRF([][]embeddings.SearchResult{listA, listB})
	want := 1.0/float64(rrfK+1) + 1.0/float64(rrfK+2) // rank 0 in one list, rank 1 in the other
	for _, key := range []string{"a", "b"} {
		if math.Abs(got[key]-want) > 1e-12 {
			t.Errorf("score[%s] = %v, want %v", key, got[key], want)
		}
	}

	// The same document twice in one list must contribute ONCE, at its best rank. Otherwise a
	// concatenated widen makes a chunk look like two channels agreeing on it.
	dupList := []embeddings.SearchResult{
		sr("a", "A.java", 1, 0, nil),
		sr("b", "B.java", 1, 0, nil),
		sr("a", "A.java", 1, 0, nil),
	}
	dup := FuseRRF([][]embeddings.SearchResult{dupList})
	wantA := 1.0 / float64(rrfK+1)
	if math.Abs(dup["a"]-wantA) > 1e-12 {
		t.Errorf("duplicate in one list scored %v, want a single contribution %v", dup["a"], wantA)
	}
}

func TestNormalizeFusedScores(t *testing.T) {
	t.Run("maps to [0,1] preserving order", func(t *testing.T) {
		in := map[string]float64{"lo": 0.005, "mid": 0.02, "hi": 0.033}
		out := normalizeFusedScores(in)
		if out["lo"] != 0 || out["hi"] != 1 {
			t.Fatalf("endpoints not mapped to [0,1]: %v", out)
		}
		if !(out["lo"] < out["mid"] && out["mid"] < out["hi"]) {
			t.Errorf("order not preserved: %v", out)
		}
		for k, v := range out {
			if v < 0 || v > 1 {
				t.Errorf("%s = %v is outside [0,1]", k, v)
			}
		}
	})
	t.Run("all tied maps to 1", func(t *testing.T) {
		out := normalizeFusedScores(map[string]float64{"a": 0.01, "b": 0.01})
		for k, v := range out {
			if v != 1 {
				t.Errorf("%s = %v; a fully tied pool must stay fully relevant so MMR falls "+
					"through to its diversity tie-break rather than zeroing everything", k, v)
			}
		}
	})
	t.Run("empty is a no-op", func(t *testing.T) {
		if got := normalizeFusedScores(map[string]float64{}); len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
}

func TestMergeByBestDistance(t *testing.T) {
	narrow := []embeddings.SearchResult{sr("a", "A.java", 1, 0.10, nil), sr("b", "B.java", 1, 0.30, nil)}
	wide := []embeddings.SearchResult{sr("b", "B.java", 1, 0.20, nil), sr("c", "C.java", 1, 0.05, nil)}

	got := mergeByBestDistance(narrow, wide)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 deduped results: %v", len(got), got)
	}
	wantOrder := []string{"c", "a", "b"} // 0.05, 0.10, 0.20 (b keeps its better distance)
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("position %d = %s, want %s (results must be ordered by distance, not "+
				"concatenated)", i, got[i].ID, want)
		}
	}
	if got[2].Distance != 0.20 {
		t.Errorf("duplicate kept distance %v, want the better 0.20", got[2].Distance)
	}
}

// Equal distances must resolve by a total order, not by arrival.
func TestMergeByBestDistance_totalOrderOnTies(t *testing.T) {
	a := []embeddings.SearchResult{sr("z", "Z.java", 9, 0.5, nil), sr("y", "A.java", 2, 0.5, nil)}
	b := []embeddings.SearchResult{sr("x", "A.java", 1, 0.5, nil)}
	got := mergeByBestDistance(a, b)
	want := []string{"x", "y", "z"} // A.java:1, A.java:2, Z.java:9
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("tie order = %s at %d, want %s; a coarse score with no unique final key is "+
				"the defect that made SearchLexical irreproducible", got[i].ID, i, want[i])
		}
	}
}

// TestMMR_relevanceSurvivesDiversityAtRRFScale is the scale regression.
//
// The defect is NOT "MMR prefers diverse results" — that is MMR working as designed. It is that raw
// RRF scores make the relevance term unable to influence the outcome at all. MMR computes
// lambda*relevance - (1-lambda)*maxSim with maxSim a cosine in [0,1]; raw RRF spans ~0.005-0.033, so
// the ENTIRE relevance range is worth ~0.014 at lambda 0.5 while a 0.1 difference in similarity is
// worth 0.05. Relevance becomes noise under any similarity difference above ~0.03.
//
// The fixture is built to sit in exactly that band: `relevant` is the most relevant candidate and
// slightly more redundant (cos 0.5); `irrelevant` is the least relevant and slightly more novel
// (cos 0.4). Correct behaviour picks `relevant`; under raw RRF the 0.1 similarity gap swamps the
// relevance gap and `irrelevant` wins.
func TestMMR_relevanceSurvivesDiversityAtRRFScale(t *testing.T) {
	query := []float32{1, 0}
	// Unit vectors whose cosine with the first pick is exactly 0.5 and 0.4.
	cos50 := []float32{0.5, 0.8660254}
	cos40 := []float32{0.4, 0.91651514}

	base := []mmrScoredChunk{
		{chunk: embeddings.Chunk{ID: "top", File: "T.java", Embedding: query}},
		{chunk: embeddings.Chunk{ID: "relevant", File: "R.java", Embedding: cos50}},
		{chunk: embeddings.Chunk{ID: "irrelevant", File: "I.java", Embedding: cos40}},
	}
	rawRRF := map[string]float64{"top": 0.0328, "relevant": 0.0320, "irrelevant": 0.0052}

	pick := func(rel map[string]float64) string {
		pool := append([]mmrScoredChunk(nil), base...)
		for i := range pool {
			pool[i].relevance = rel[pool[i].chunk.ID]
		}
		sortMMRPool(pool)
		got := maximalMarginalRelevance(query, pool, 2, defaultSimilarMMRLambda)
		if len(got) != 2 {
			t.Fatalf("picked %d, want 2", len(got))
		}
		return got[1].ID
	}

	// Raw RRF: relevance cannot compete, so the least relevant candidate wins on a 0.1 similarity
	// edge. This is the shipped behaviour and the reason `fusion: rrf` measured as a regression.
	if got := pick(rawRRF); got != "irrelevant" {
		t.Logf("raw RRF second pick = %s; the failure mode this test documents may have changed, "+
			"revisit the scale argument before relaxing anything", got)
	}

	// Rescaled to [0,1]: relevance is commensurate with the diversity term again.
	if got := pick(normalizeFusedScores(rawRRF)); got != "relevant" {
		t.Errorf("second pick = %s, want relevant; after rescaling, the most relevant candidate "+
			"must beat a near-zero-relevance one that is only marginally more novel", got)
	}
}

// An embedding-less candidate scores maxSim 0 against everything, i.e. maximally novel. This pins
// why the lexical channel must return vectors.
func TestMMR_missingEmbeddingWinsDiversityOutright(t *testing.T) {
	along := []float32{1, 0}
	pool := []mmrScoredChunk{
		{chunk: embeddings.Chunk{ID: "top", File: "T.java", Embedding: along}, relevance: 1.0},
		{chunk: embeddings.Chunk{ID: "relevant-similar", File: "S.java", Embedding: along}, relevance: 0.9},
		{chunk: embeddings.Chunk{ID: "no-embedding", File: "N.java"}, relevance: 0.1},
	}
	sortMMRPool(pool)
	picked := maximalMarginalRelevance(along, pool, 2, defaultSimilarMMRLambda)
	if len(picked) != 2 {
		t.Fatalf("picked %d", len(picked))
	}
	if picked[1].ID != "no-embedding" {
		t.Skip("MMR no longer prefers embedding-less candidates; the invariant this documents has changed")
	}
	t.Log("confirmed: a candidate with no embedding beats a far more relevant one on diversity " +
		"alone — which is why lexicalChannel must request embeddings")
}

// fakeLexicalReader implements ChunkReader plus SearchLexical so the fused pool can be inspected.
type fakeLexicalReader struct {
	dense   []embeddings.SearchResult
	lexical []embeddings.SearchResult
	gotOpts embeddings.SearchOptions
}

func (f *fakeLexicalReader) List(context.Context, embeddings.ListOptions) ([]embeddings.Chunk, error) {
	return nil, nil
}
func (f *fakeLexicalReader) Search(context.Context, []float32, embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	return f.dense, nil
}
func (f *fakeLexicalReader) SearchLexical(_ context.Context, _ string, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	f.gotOpts = opts
	return f.lexical, nil
}

// The lexical channel must ask for embeddings. SearchOptions.OmitEmbedding is documented as unsafe
// for anything reaching MMR, and these results go straight into the MMR pool.
func TestLexicalChannel_requestsEmbeddings(t *testing.T) {
	f := &fakeLexicalReader{lexical: []embeddings.SearchResult{sr("lx", "L.java", 1, 0, []float32{0, 1})}}
	target := &embeddings.Chunk{ID: "t", File: "T.java", Embedding: []float32{1, 0}}
	req := ContextRequest{RepoID: "r", Lang: "java", Fusion: "rrf", LexicalQuery: "OwnerController"}

	out := lexicalChannel(context.Background(), f, req, target, 10, "")
	if len(out) != 1 {
		t.Fatalf("lexicalChannel returned %d results", len(out))
	}
	if f.gotOpts.OmitEmbedding {
		t.Error("lexicalChannel asked SearchLexical to omit embeddings; those chunks enter the MMR " +
			"pool, where a nil vector reads as maximally diverse and displaces dense hits")
	}
}

// The lexical channel is the one similar-chunk path with no chunk-type filter: the dense channel
// iterates the profile allowlist (which can never name dependency_doc — see
// TestProfiles_NeverEnumerateDependencyDocs), but a synthesized lexical query carries framework
// vocabulary that ingested dependency documentation (B55) matches heavily. This pins the exclusion
// that keeps library text out of the MMR pool.
func TestLexicalChannel_excludesDependencyDocs(t *testing.T) {
	f := &fakeLexicalReader{lexical: []embeddings.SearchResult{sr("lx", "L.java", 1, 0, []float32{0, 1})}}
	target := &embeddings.Chunk{ID: "t", File: "T.java", Embedding: []float32{1, 0}}
	req := ContextRequest{RepoID: "r", Lang: "java", Fusion: "rrf", LexicalQuery: "Pageable findAll"}

	if out := lexicalChannel(context.Background(), f, req, target, 10, ""); len(out) != 1 {
		t.Fatalf("lexicalChannel returned %d results", len(out))
	}
	if f.gotOpts.ExcludeChunkType != embeddings.ChunkTypeDependencyDoc {
		t.Errorf("lexical channel ExcludeChunkType = %q; want %q — without it dependency docs enter the MMR pool",
			f.gotOpts.ExcludeChunkType, embeddings.ChunkTypeDependencyDoc)
	}
}
