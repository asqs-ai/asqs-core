// Package tokens counts prompt tokens so context assembly can be budgeted.
//
// Before this existed there was no token accounting anywhere in the repository:
// `grep -rn "tiktoken\|CountTokens\|TokenBudget"` returned nothing, every `MaxTokens` was an
// *output* cap, and `FormatOptions.MaxChunkChars` was left at 0 by every caller. The generation
// prompt was therefore unbounded in three independent dimensions — chunk body size, section item
// counts, and the prepended blocks (of which the entire existing test file, at the very top of an
// extend-existing run, is the worst). Overflow was handled by the provider: silent truncation of
// the *input*, which drops the output contract sitting last, or a 400. Both present as "the model
// produced garbage".
package tokens

import (
	"strings"
	"unicode/utf8"
)

// Counter estimates the token count of a string for a specific model family.
type Counter interface {
	// Count returns the estimated number of tokens in s.
	Count(s string) int
	// Name identifies the counting strategy for telemetry (e.g. "heuristic:3.0").
	Name() string
}

// charsPerTokenCode is the divisor used by the heuristic counter.
//
// Source code averages roughly 3.2 characters per token across the four supported languages
// (identifiers split into sub-word pieces, punctuation is dense). 3.0 deliberately *over*-estimates:
// a budget that thinks the prompt is larger than it is wastes a little context, while one that
// under-estimates overflows the window — and overflow is the failure this package exists to prevent.
const charsPerTokenCode = 3.0

// heuristic counts by character count divided by a fixed ratio.
type heuristic struct{ divisor float64 }

func (h heuristic) Count(s string) int {
	if s == "" {
		return 0
	}
	d := h.divisor
	if d <= 0 {
		d = charsPerTokenCode
	}
	// Count runes, not bytes: a byte-based estimate over-counts multi-byte source (comments and
	// string literals in non-ASCII languages) by up to 3x and would shrink budgets arbitrarily.
	n := float64(utf8.RuneCountInString(s))
	c := int(n/d) + 1
	return c
}

func (h heuristic) Name() string {
	if h.divisor == charsPerTokenCode {
		return "heuristic:3.0"
	}
	return "heuristic"
}

// For returns a Counter for the given provider and model.
//
// Every provider currently uses the calibrated character heuristic. A real BPE tokenizer
// (tiktoken-go for the OpenAI family) is a drop-in replacement here — the interface exists so
// adding it does not touch any caller — but it is deliberately not a dependency yet: the budget's
// job is to prevent overflow, an over-estimating heuristic does that safely, and the accuracy of
// the estimate is measurable against Usage.PromptTokens before paying for ~2 MB of BPE tables.
// See CalibrationDelta.
func For(provider, model string) Counter {
	return heuristic{divisor: charsPerTokenCode}
}

// CountAll returns the total token count of all parts.
func CountAll(c Counter, parts ...string) int {
	total := 0
	for _, p := range parts {
		total += c.Count(p)
	}
	return total
}

// CalibrationDelta returns the signed relative error of an estimate against a provider-reported
// count: positive means the estimate was high (safe), negative means it was low (dangerous).
//
// Callers record this whenever Usage.PromptTokens is available so the heuristic's accuracy is a
// measured number rather than an assumption. A persistently negative delta means the divisor is too
// large and budgets are overflowing.
func CalibrationDelta(estimated, actual int) float64 {
	if actual <= 0 {
		return 0
	}
	return float64(estimated-actual) / float64(actual)
}

// ClampToTokens truncates s to at most maxTokens, cutting on a line boundary so a code block is
// never split mid-line. It returns the kept text and the number of lines removed.
//
// Rune-safe by construction (it slices whole lines). The previous truncator sliced *bytes*
// (`content[:maxChars]`), which splits a multi-byte rune and emits invalid UTF-8 — unreachable only
// because MaxChunkChars was always 0, and this package is what makes it reachable.
func ClampToTokens(s string, maxTokens int, c Counter) (kept string, elidedLines int) {
	if s == "" || maxTokens <= 0 {
		return "", strings.Count(s, "\n")
	}
	if c.Count(s) <= maxTokens {
		return s, 0
	}
	lines := strings.Split(s, "\n")
	var b strings.Builder
	used := 0
	for i, ln := range lines {
		lineTokens := c.Count(ln) + 1 // +1 approximates the newline
		if used+lineTokens > maxTokens {
			return b.String(), len(lines) - i
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(ln)
		used += lineTokens
	}
	return b.String(), 0
}
