package llembed

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaxInputRunes_knownModels(t *testing.T) {
	// OpenAI: the old flat 30 000-rune cap was ~8 000 tokens, i.e. at or OVER the 8191 limit — so it
	// did not reliably prevent the error it existed to prevent, and pushed inputs into the
	// binary-bisection oversize fallback it was meant to avoid.
	openai := MaxInputRunes("openai", "text-embedding-3-small")
	if openai >= MaxEmbeddingInputRunes {
		t.Errorf("text-embedding-3-small cap = %d, must be below the old flat %d", openai, MaxEmbeddingInputRunes)
	}

	// Small local models are the dangerous case: Ollama is called with truncate:true, so a 30 000-rune
	// chunk sent to a 512-token model was embedded from roughly its first 6%, silently.
	small := MaxInputRunes("ollama", "all-minilm")
	if small > 1000 {
		t.Errorf("all-minilm cap = %d runes; a 256-token model must get a much smaller cap", small)
	}
	if small >= openai {
		t.Errorf("a 256-token model (%d) should get a smaller cap than an 8191-token one (%d)", small, openai)
	}
}

// An unknown model must degrade to the conservative default, not to the previous permissive cap.
func TestMaxInputRunes_unknownModelIsConservative(t *testing.T) {
	got := MaxInputRunes("ollama", "some-model-nobody-has-heard-of")
	want := MaxInputRunes("", "")
	if got != want {
		t.Errorf("unknown model cap = %d, want the conservative default %d", got, want)
	}
	if got > 2000 {
		t.Errorf("unknown model cap %d is not conservative; over-estimating silently embeds a fraction of the chunk", got)
	}
}

func TestMaxInputRunes_handlesOllamaTagsAndVersions(t *testing.T) {
	base := MaxInputRunes("ollama", "nomic-embed-text")
	tagged := MaxInputRunes("ollama", "nomic-embed-text:v1.5")
	if base != tagged {
		t.Errorf("a model tag should not change the cap: %d vs %d", base, tagged)
	}
	if MaxInputRunes("openai", "text-embedding-3-small-v2") != MaxInputRunes("openai", "text-embedding-3-small") {
		t.Error("a versioned suffix should resolve to the same family cap")
	}
	if MaxInputRunes("", "") != MaxInputRunes("", "unknown") {
		t.Error("an empty model should use the conservative default")
	}
}

func TestNormalizeTextsWithLimit_countsTruncations(t *testing.T) {
	long := strings.Repeat("x", 500)
	got, truncated := NormalizeTextsWithLimit([]string{"short", long, "  padded  "}, 100)

	if truncated != 1 {
		t.Errorf("truncated = %d, want 1", truncated)
	}
	if utf8.RuneCountInString(got[1]) != 100 {
		t.Errorf("long input was not clamped to the limit: %d runes", utf8.RuneCountInString(got[1]))
	}
	if got[0] != "short" {
		t.Errorf("short input was modified: %q", got[0])
	}
	if got[2] != "padded" {
		t.Errorf("input should be trimmed: %q", got[2])
	}
}

func TestNormalizeTextsWithLimit_isRuneSafe(t *testing.T) {
	// Truncating by bytes would split a multi-byte rune; the cap is in runes.
	multibyte := strings.Repeat("ü", 300)
	got, _ := NormalizeTextsWithLimit([]string{multibyte}, 50)
	if !utf8.ValidString(got[0]) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if utf8.RuneCountInString(got[0]) != 50 {
		t.Errorf("got %d runes, want 50", utf8.RuneCountInString(got[0]))
	}
}

func TestTruncationCounter(t *testing.T) {
	before := TruncationCount()
	NoteTruncated(3)
	NoteTruncated(0) // must not move the counter
	if got := TruncationCount() - before; got != 3 {
		t.Errorf("counter advanced by %d, want 3", got)
	}
}

// The counter is diagnostic in its own right: the chunker targets ~3 200 runes, so a non-zero count
// on a normal repo means the chunker is emitting oversized chunks — a separate bug worth knowing
// about, and previously invisible.
func TestNormalizeTextsForModel_typicalChunkIsNotTruncated(t *testing.T) {
	typicalChunk := strings.Repeat("    doSomething(withArgs);\n", 100) // ~2 600 runes
	_, truncated := NormalizeTextsForModel([]string{typicalChunk}, "openai", "text-embedding-3-small")
	if truncated != 0 {
		t.Errorf("a typical primary chunk was truncated; the cap is too tight for the chunker's target size")
	}
}
