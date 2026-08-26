package indexer

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDefaultSanitizeOptions(t *testing.T) {
	opts := DefaultSanitizeOptions()
	if opts.MaxCommentRunes != 500 {
		t.Errorf("MaxCommentRunes = %d; want 500", opts.MaxCommentRunes)
	}
	if !opts.StripBlockComments {
		t.Error("StripBlockComments want true")
	}
	if opts.NormalizeWhitespace {
		t.Error("NormalizeWhitespace want false by default")
	}
}

func TestSanitize_stripBlockComments(t *testing.T) {
	opts := DefaultSanitizeOptions()
	opts.StripBlockComments = true
	input := "code /* comment */ more"
	out := Sanitize(input, opts)
	if out != "code  more" {
		t.Errorf("Sanitize(strip block) = %q; want \"code  more\"", out)
	}
}

func TestSanitize_stripBlockComments_multiline(t *testing.T) {
	opts := DefaultSanitizeOptions()
	opts.StripBlockComments = true
	input := "a\n/**\n * doc\n */\nb"
	out := Sanitize(input, opts)
	if out != "a\n\nb" {
		t.Errorf("Sanitize(multiline block) = %q; want \"a\\n\\nb\"", out)
	}
}

func TestSanitize_truncateLongComments(t *testing.T) {
	opts := DefaultSanitizeOptions()
	opts.StripBlockComments = false
	opts.MaxCommentRunes = 10
	input := "code\n// this is a very long comment that should be truncated"
	out := Sanitize(input, opts)
	if !strings.Contains(out, "code") {
		t.Errorf("expected code line preserved: %q", out)
	}
	// Truncation adds "…" to long // lines
	if !strings.Contains(out, "…") {
		t.Errorf("expected long // comment truncated with …: %q", out)
	}
}

func TestSanitize_disallowPatterns(t *testing.T) {
	opts := DefaultSanitizeOptions()
	opts.DisallowPatterns = []*regexp.Regexp{regexp.MustCompile(`SECRET|password`)}
	input := "key = SECRET; p = password"
	out := Sanitize(input, opts)
	// Sanitize trims space at end, so trailing space after replacement is removed
	if out != "key = ; p =" {
		t.Errorf("Sanitize(disallow) = %q; want \"key = ; p =\"", out)
	}
}

func TestSanitize_normalizeWhitespace(t *testing.T) {
	opts := DefaultSanitizeOptions()
	opts.NormalizeWhitespace = true
	input := "a   b\n\n\tc"
	out := Sanitize(input, opts)
	if out != "a b c" {
		t.Errorf("Sanitize(normalize) = %q; want \"a b c\"", out)
	}
}

func TestSanitize_trimSpace(t *testing.T) {
	opts := DefaultSanitizeOptions()
	input := "  x  "
	out := Sanitize(input, opts)
	if out != "x" {
		t.Errorf("Sanitize(trim) = %q; want \"x\"", out)
	}
}

func TestSanitize_emptyOptions(t *testing.T) {
	opts := SanitizeOptions{}
	out := Sanitize("hello", opts)
	if out != "hello" {
		t.Errorf("Sanitize(empty opts) = %q; want \"hello\"", out)
	}
}

func TestSanitize_combined(t *testing.T) {
	opts := SanitizeOptions{
		StripBlockComments:  true,
		MaxCommentRunes:     5,
		NormalizeWhitespace: true,
	}
	input := "  a  /* remove */ b  \n\n  // short  "
	out := Sanitize(input, opts)
	// Block removed, normalize space, trim, // line truncated to 5 runes + …
	if out == "" {
		t.Error("combined Sanitize returned empty")
	}
	if len(out) < 3 {
		t.Errorf("combined result too short: %q", out)
	}
}

func TestSanitize_removesNULAndNormalizesInvalidUTF8(t *testing.T) {
	opts := SanitizeOptions{}
	in := string([]byte{'a', 0x00, 'b', 0xff, 'c'})
	out := Sanitize(in, opts)
	if strings.ContainsRune(out, rune(0)) {
		t.Fatalf("sanitize should remove NUL byte, got %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("sanitize should return valid UTF-8, got bytes=%v", []byte(out))
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") || !strings.Contains(out, "c") {
		t.Fatalf("sanitize unexpectedly lost core content: %q", out)
	}
}
