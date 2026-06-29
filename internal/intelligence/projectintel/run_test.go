package projectintel

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// fakeChat returns a preset summary string.
type fakeChat struct {
	resp string
	err  error
}

func (f *fakeChat) Complete(_ context.Context, _ []model.Message, _ model.CompleteOptions) (*model.CompleteResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &model.CompleteResult{Content: f.resp}, nil
}

// fakeEmbedder returns deterministic unit vectors per input text (first char code → float32).
type fakeEmbedder struct{ err error }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := float32(1)
		if len(t) > 0 {
			v = float32(t[0])
		}
		out[i] = []float32{v, 0}
	}
	return out, nil
}

func baseOpts() Options {
	return Options{
		Enabled:           true,
		MinRelevanceScore: 0, // no floor so test docs pass
		CacheEnabled:      false,
	}
}

func TestRun_DiscoverAndRankDocs(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test junit mockito")
	writeRepoFile(t, dir, "CHANGELOG.md", "v1.0 released")

	res, err := Run(context.Background(), Input{
		RepoAbs: dir,
		Lang:    "java",
		Opts:    baseOpts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode == "off" {
		t.Fatal("expected non-off mode")
	}
	if res.DocsSelected == 0 {
		t.Fatal("expected at least one doc selected")
	}
	if !strings.Contains(res.Snapshot.Markdown, "README.md") {
		t.Fatalf("expected README.md in markdown, got:\n%s", res.Snapshot.Markdown)
	}
}

func TestRun_CandidatesHaveEmbeddings(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test junit")

	res, err := Run(context.Background(), Input{
		RepoAbs:  dir,
		Lang:     "java",
		Embedder: &fakeEmbedder{},
		Opts: Options{
			Enabled:           true,
			MinRelevanceScore: 0,
			CacheEnabled:      false,
			UseEmbeddingsRank: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "embedding" {
		t.Fatalf("expected embedding mode, got %s", res.Mode)
	}
	if len(res.Candidates) == 0 {
		t.Fatal("expected candidates")
	}
	if len(res.Candidates[0].DocEmbedding) == 0 {
		t.Fatal("expected non-nil DocEmbedding")
	}
}

func TestRun_EmbedderError_GracefulDegradation(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test")

	res, err := Run(context.Background(), Input{
		RepoAbs:  dir,
		Lang:     "java",
		Embedder: &fakeEmbedder{err: context.DeadlineExceeded},
		Opts: Options{
			Enabled:           true,
			MinRelevanceScore: 0,
			CacheEnabled:      false,
			UseEmbeddingsRank: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode == "embedding" {
		t.Fatal("should not be embedding mode on embedder error")
	}
}

func TestRun_UseEmbeddingsRankDisabled_NoEmbedderCall(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test")

	called := false
	emb := &countingEmbedder{called: &called}
	_, err := Run(context.Background(), Input{
		RepoAbs:  dir,
		Lang:     "java",
		Embedder: emb,
		Opts: Options{
			Enabled:           true,
			MinRelevanceScore: 0,
			CacheEnabled:      false,
			UseEmbeddingsRank: false, // disabled
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("embedder should not be called when UseEmbeddingsRank=false")
	}
}

type countingEmbedder struct{ called *bool }

func (c *countingEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	*c.called = true
	return nil, nil
}

func TestRun_DiskCacheHit(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test")

	opts := Options{
		Enabled:           true,
		MinRelevanceScore: 0,
		CacheEnabled:      true,
		CachePath:         ".asqs/pi-cache-test.json",
	}
	in := Input{RepoAbs: dir, Lang: "java", Opts: opts}

	// First run: cache miss
	r1, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r1.CacheHit {
		t.Fatal("first run should not be a cache hit")
	}

	// Second run: cache hit
	r2, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.CacheHit {
		t.Fatal("second run should be a cache hit")
	}
}

func TestRun_OpenAPIFileSurfaces(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "api/openapi.yaml", "openapi: 3.0.0\ninfo:\n  title: test\npaths: {}")

	res, err := Run(context.Background(), Input{
		RepoAbs: dir,
		Lang:    "java",
		Opts:    baseOpts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Candidates {
		if c.RelPath == "api/openapi.yaml" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected openapi.yaml in candidates, got %v", res.Candidates)
	}
}

func TestRun_SQLFileSurfaces(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "db/schema.sql", "CREATE TABLE users (id INT);")

	res, err := Run(context.Background(), Input{
		RepoAbs: dir,
		Lang:    "java",
		Opts:    baseOpts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Candidates {
		if c.RelPath == "db/schema.sql" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected schema.sql in candidates, got %v", res.Candidates)
	}
}

func TestRun_SkipFlagDisables(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "README.md", "java test")

	res, err := Run(context.Background(), Input{
		RepoAbs: dir,
		Skip:    true,
		Opts:    Options{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "off" {
		t.Fatalf("expected off mode when Skip=true, got %s", res.Mode)
	}
}
