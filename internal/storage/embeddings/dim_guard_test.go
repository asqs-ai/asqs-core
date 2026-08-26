package embeddings

import (
	"os"
	"strings"
	"testing"
)

// The opt-in must be explicit and must not be satisfied by an accidentally-set-but-empty variable,
// or a stray `export ASQS_ALLOW_EMBEDDING_DIM_RESET=` in a shell profile would re-arm the footgun
// for every command in that session.
func TestEmbeddingDimResetAllowed(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {" ", false}, {"maybe", false},
	} {
		t.Setenv(EnvAllowEmbeddingDimReset, tc.val)
		if got := embeddingDimResetAllowed(); got != tc.want {
			t.Errorf("%s=%q: allowed=%v, want %v", EnvAllowEmbeddingDimReset, tc.val, got, tc.want)
		}
	}
	os.Unsetenv(EnvAllowEmbeddingDimReset)
	if embeddingDimResetAllowed() {
		t.Error("unset must not allow the reset")
	}
}

// alignChunksEmbeddingColumn needs a live server, so this asserts the shape of the guard in source:
// a populated table must be counted and refused before anything destructive runs.
//
// The stakes are why this is worth a source-level guard. The statement is `TRUNCATE TABLE chunks` —
// every repo in the database, not just the one being indexed — and it executes from InitSchema on
// process start, before the command does any work of its own. This repository ships configs at both
// 768 and 1536 dimensions, so pointing -config at the wrong stack used to silently destroy the
// entire corpus, taking any ireval golden suite with it (labels are chunk UUIDs).
func TestAlignChunksEmbeddingColumn_refusesToTruncateAPopulatedCorpus(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	var code []string
	for _, ln := range strings.Split(string(b), "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		code = append(code, ln)
	}
	src := strings.Join(code, "\n")

	fn := src[strings.Index(src, "func (s *Store) alignChunksEmbeddingColumn"):]
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}

	countAt := strings.Index(fn, "SELECT count(*) FROM chunks")
	guardAt := strings.Index(fn, "embeddingDimResetAllowed()")
	truncAt := strings.Index(fn, "TRUNCATE TABLE chunks")

	switch {
	case countAt < 0:
		t.Fatal("alignChunksEmbeddingColumn no longer counts existing chunks before realigning")
	case guardAt < 0:
		t.Fatal("alignChunksEmbeddingColumn no longer gates the truncate on an explicit opt-in")
	case truncAt < 0:
		t.Skip("no truncate in this version; guard is moot")
	case countAt > truncAt || guardAt > truncAt:
		t.Error("the row count and the opt-in check must both precede TRUNCATE TABLE chunks; " +
			"otherwise a wrong -config destroys every repo in the database before anything can stop it")
	}
}
