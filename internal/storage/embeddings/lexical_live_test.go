package embeddings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func lexicalScratchStore(t *testing.T, dim int) (*Store, context.Context) {
	t.Helper()
	url, why := metadata.ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(ctx, Config{ConnString: url, Dimension: dim})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func seedLexicalChunk(t *testing.T, s *Store, ctx context.Context, dim int, content, file string) {
	t.Helper()
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = 0.02
	}
	if _, err := s.InsertChunk(ctx, &Chunk{
		Content: content, Embedding: vec, File: file, Lang: "java",
		ChunkType: "test", StartLine: 1, EndLine: 9, RepoID: "github.com/acme/lexical",
	}); err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}
}

// TestSearchLexical_returnsEmbeddingsUnlessOmitted is the regression test for the nil-embedding half
// of the RRF defect.
//
// SearchLexical never selected the embedding column and ignored opts.OmitEmbedding entirely, so
// every lexical hit reached the MMR pool with a nil vector. MMR scores diversity as cosine against
// already-picked chunks, and cosine against nil is 0 — "maximally novel" — so lexical-only hits
// displaced dense hits wholesale. On the PetClinic corpus that was 26-32 of a 66-72 chunk pool.
func TestSearchLexical_returnsEmbeddingsUnlessOmitted(t *testing.T) {
	const dim = 768
	s, ctx := lexicalScratchStore(t, dim)
	if _, err := s.pool.Exec(ctx, `DELETE FROM chunks WHERE repo_id = $1`, "github.com/acme/lexical"); err != nil {
		t.Fatal(err)
	}
	seedLexicalChunk(t, s, ctx, dim, "class OwnerControllerTests { void testProcessCreationForm() {} }", "src/test/OwnerControllerTests.java")
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM chunks WHERE repo_id = $1`, "github.com/acme/lexical")
	})

	opts := SearchOptions{RepoID: "github.com/acme/lexical", Limit: 10}
	withVec, err := s.SearchLexical(ctx, "OwnerControllerTests", opts)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(withVec) == 0 {
		t.Fatal("no lexical hits for a token that is literally in the content")
	}
	for i, r := range withVec {
		if len(r.Embedding) != dim {
			t.Fatalf("result %d has embedding length %d, want %d; a lexical hit with no vector is "+
				"scored by MMR as maximally diverse and crowds out dense hits", i, len(r.Embedding), dim)
		}
	}

	optsOmit := opts
	optsOmit.OmitEmbedding = true
	omitted, err := s.SearchLexical(ctx, "OwnerControllerTests", optsOmit)
	if err != nil {
		t.Fatalf("SearchLexical(OmitEmbedding): %v", err)
	}
	if len(omitted) != len(withVec) {
		t.Fatalf("OmitEmbedding changed the result count: %d vs %d", len(omitted), len(withVec))
	}
	for i, r := range omitted {
		if len(r.Embedding) != 0 {
			t.Errorf("result %d carries an embedding despite OmitEmbedding; the flag must still "+
				"work for callers that genuinely want ranks only", i)
		}
	}
}

// A real query failure must surface, not masquerade as "no lexical results".
//
// Returning (nil, nil) for every error is the same silent-nothing mode that made the channel inert
// for months: `fusion: rrf` quietly becomes `fusion: dense` and the A/B built on it means nothing.
func TestSearchLexical_surfacesRealQueryErrors(t *testing.T) {
	const dim = 768
	s, ctx := lexicalScratchStore(t, dim)

	// A cancelled context is a genuine failure the store must not swallow.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.SearchLexical(cancelled, "OwnerController", SearchOptions{RepoID: "x", Limit: 5})
	if err == nil {
		t.Fatal("SearchLexical returned nil error for a cancelled context; a failed lexical query " +
			"must not be indistinguishable from an empty result set")
	}
	if errors.Is(err, ErrLexicalIndexUnavailable) {
		t.Errorf("a cancelled context was classified as 'no lexical index': %v", err)
	}
}

// A corpus without content_tsv is the ONE benign failure, and it must be distinguishable so the
// caller can fall back silently instead of counting it as a defect.
func TestSearchLexical_missingColumnIsClassified(t *testing.T) {
	const dim = 768
	s, ctx := lexicalScratchStore(t, dim)

	// Simulate a pre-lexical corpus by hiding the generated column.
	if _, err := s.pool.Exec(ctx, `ALTER TABLE chunks RENAME COLUMN content_tsv TO content_tsv_hidden`); err != nil {
		t.Skipf("cannot rename content_tsv (generated column may not exist here): %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `ALTER TABLE chunks RENAME COLUMN content_tsv_hidden TO content_tsv`)
	})

	_, err := s.SearchLexical(ctx, "OwnerController", SearchOptions{RepoID: "x", Limit: 5})
	if !errors.Is(err, ErrLexicalIndexUnavailable) {
		t.Fatalf("missing content_tsv must classify as ErrLexicalIndexUnavailable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Errorf("the error should tell the operator what to run, got: %v", err)
	}
}
