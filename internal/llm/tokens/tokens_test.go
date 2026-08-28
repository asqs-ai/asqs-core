package tokens

import (
	"strings"
	"testing"
)

func TestHeuristicCounter_overEstimates(t *testing.T) {
	c := For("openai", "gpt-4o")
	// The divisor deliberately over-estimates: a budget that thinks the prompt is larger than it is
	// wastes a little context; one that under-estimates overflows the window, which is the failure
	// this package exists to prevent.
	code := "public Order place(Order o) { return repository.save(o); }"
	got := c.Count(code)
	roughActual := len(code) / 4 // typical real BPE ratio for code
	if got < roughActual {
		t.Errorf("Count(%q) = %d, which is below the ~%d a real tokenizer would give; the estimate must be conservative",
			code, got, roughActual)
	}
}

func TestCounter_countsRunesNotBytes(t *testing.T) {
	// A byte-based estimate over-counts multi-byte source by up to 3x, shrinking budgets
	// arbitrarily for any repo with non-ASCII comments or string literals.
	c := For("", "")
	ascii := strings.Repeat("a", 300)
	multibyte := strings.Repeat("ü", 300) // 600 bytes, 300 runes
	if c.Count(ascii) != c.Count(multibyte) {
		t.Errorf("Count differs for equal rune counts: ascii=%d multibyte=%d", c.Count(ascii), c.Count(multibyte))
	}
}

func TestCounter_empty(t *testing.T) {
	if got := For("", "").Count(""); got != 0 {
		t.Errorf("Count(\"\") = %d, want 0", got)
	}
}

func TestClampToTokens_cutsOnLineBoundary(t *testing.T) {
	c := For("", "")
	src := "line one is here\nline two is here\nline three is here\nline four is here\n"
	kept, elided := ClampToTokens(src, c.Count(src)/2, c)

	if strings.Contains(kept, "line four") {
		t.Error("clamp kept more than the allowance")
	}
	if elided == 0 {
		t.Error("elided line count should be non-zero when content was cut")
	}
	// Never cut mid-line: every retained line must be complete.
	for _, ln := range strings.Split(kept, "\n") {
		if ln == "" {
			continue
		}
		if !strings.Contains(src, ln) {
			t.Errorf("clamp produced a partial line: %q", ln)
		}
	}
}

// TestClampToTokens_isRuneSafe is the regression test for M-14: the previous truncator sliced bytes
// (`content[:maxChars]`), which splits a multi-byte rune and emits invalid UTF-8.
func TestClampToTokens_isRuneSafe(t *testing.T) {
	c := For("", "")
	src := strings.Repeat("héllo wörld ünicode\n", 50)
	for _, budget := range []int{1, 5, 17, 40, 123} {
		kept, _ := ClampToTokens(src, budget, c)
		if !isValidUTF8(kept) {
			t.Fatalf("budget %d produced invalid UTF-8", budget)
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestClampToTokens_noopWhenUnderBudget(t *testing.T) {
	c := For("", "")
	src := "short\ntext\n"
	kept, elided := ClampToTokens(src, 1000, c)
	if kept != src || elided != 0 {
		t.Errorf("under-budget input was modified: kept=%q elided=%d", kept, elided)
	}
}

func TestCalibrationDelta(t *testing.T) {
	// Positive = the estimate was high, which is the safe direction.
	if d := CalibrationDelta(120, 100); d <= 0 {
		t.Errorf("over-estimate should give a positive delta, got %v", d)
	}
	if d := CalibrationDelta(80, 100); d >= 0 {
		t.Errorf("under-estimate should give a negative delta, got %v", d)
	}
	if d := CalibrationDelta(100, 0); d != 0 {
		t.Errorf("no actual measurement should give 0, got %v", d)
	}
}
