package llembed

import (
	"context"
	"errors"
	"math"
	"testing"
)

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func TestL2Normalize_producesUnitVectors(t *testing.T) {
	cases := [][]float32{
		{3, 4},                // norm 5
		{1, 0, 0, 0},          // already unit
		{-2, -2, -2, -2},      // negative components
		{0.001, 0.002, 0.003}, // small magnitudes
		{1e6, 2e6},            // large magnitudes
	}
	for _, v := range cases {
		got := L2Normalize(v)
		if n := norm(got); math.Abs(n-1) > 1e-6 {
			t.Errorf("L2Normalize(%v) has norm %v, want 1", v, n)
		}
		if !IsUnitNorm(got, 1e-6) {
			t.Errorf("IsUnitNorm rejected a normalized vector: %v", got)
		}
	}
}

// Normalization must preserve direction — that is the whole point, since cosine depends only on
// direction and the migration relies on in-place scaling being equivalent to a re-embed.
func TestL2Normalize_preservesDirection(t *testing.T) {
	a := []float32{3, 4}
	scaled := []float32{30, 40} // same direction, 10x magnitude
	na, ns := L2Normalize(a), L2Normalize(scaled)
	for i := range na {
		if math.Abs(float64(na[i]-ns[i])) > 1e-6 {
			t.Fatalf("direction not preserved: %v vs %v", na, ns)
		}
	}
}

func TestL2Normalize_zeroVectorUnchanged(t *testing.T) {
	z := []float32{0, 0, 0}
	got := L2Normalize(z)
	for _, x := range got {
		if x != 0 {
			t.Fatalf("zero vector was modified: %v", got)
		}
	}
	if IsUnitNorm(z, 1e-6) {
		t.Error("a zero vector must not report as unit norm")
	}
	if L2Normalize(nil) != nil {
		t.Error("nil input should return nil")
	}
}

func TestL2Normalize_doesNotMutateInput(t *testing.T) {
	in := []float32{3, 4}
	_ = L2Normalize(in)
	if in[0] != 3 || in[1] != 4 {
		t.Fatalf("input was mutated: %v", in)
	}
}

type fakeEmbedder struct {
	vecs [][]float32
	err  error
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f.vecs, f.err
}

// The wrapper is what makes the guarantee hold for every provider rather than only those that
// happen to emit unit vectors (OpenAI does; local models served via Ollama generally do not).
func TestNormalizingEmbedder_normalizesEveryVector(t *testing.T) {
	inner := &fakeEmbedder{vecs: [][]float32{{3, 4}, {0, 5}, {0, 0}}}
	e := NewNormalizingEmbedder(inner)

	got, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	for i, v := range got[:2] {
		if n := norm(v); math.Abs(n-1) > 1e-6 {
			t.Errorf("vector %d has norm %v, want 1", i, n)
		}
	}
	// The zero vector has no direction to preserve and stays zero rather than becoming NaN.
	for _, x := range got[2] {
		if math.IsNaN(float64(x)) {
			t.Fatal("zero vector normalized to NaN")
		}
	}
}

func TestNormalizingEmbedder_propagatesError(t *testing.T) {
	sentinel := errors.New("provider down")
	e := NewNormalizingEmbedder(&fakeEmbedder{err: sentinel})
	if _, err := e.Embed(context.Background(), []string{"a"}); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the inner error", err)
	}
}

func TestNewNormalizingEmbedder_idempotentAndNilSafe(t *testing.T) {
	if NewNormalizingEmbedder(nil) != nil {
		t.Error("nil inner should produce nil")
	}
	once := NewNormalizingEmbedder(&fakeEmbedder{})
	twice := NewNormalizingEmbedder(once)
	if once != twice {
		t.Error("double-wrapping should return the existing wrapper, not nest it")
	}
}
