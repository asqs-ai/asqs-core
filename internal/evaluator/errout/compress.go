package errout

import (
	"fmt"
	"strings"
)

const (
	CompressionNone       = "none"
	CompressionDeduped    = "deduped"
	CompressionHeadTail   = "head_tail"
	CompressionLLMSummary = "llm_summary"
)

// DedupeConsecutiveLines collapses long runs of identical consecutive lines (typical in vstest/dotnet
// stress output). When a line repeats more than threshold times in a row, only the first copy is kept
// plus a single marker counting the remainder.
func DedupeConsecutiveLines(s string, threshold int) string {
	if threshold < 1 {
		threshold = 3
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= 1 {
		return s
	}
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		j := i + 1
		for j < len(lines) && lines[j] == line {
			j++
		}
		runLen := j - i
		if runLen > threshold {
			out = append(out, line)
			out = append(out, fmt.Sprintf("[... %d identical lines omitted ...]", runLen-1))
		} else {
			for k := 0; k < runLen; k++ {
				out = append(out, line)
			}
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// CanonicalForFixLoop applies Sanitize then toolchain-specific deduplication used for fix-loop
// signatures and fixer input normalization (csharp/cs only).
func CanonicalForFixLoop(lang, raw string) string {
	s := Sanitize(lang, raw)
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		return DedupeConsecutiveLines(s, 3)
	default:
		return s
	}
}

// CompressForAudit applies deterministic shrinking suitable for audit rows. Expect
// sanitizedCanonical to already include toolchain-specific dedupe from CanonicalForFixLoop.
func CompressForAudit(_ string, sanitizedCanonical string, maxRunes int) (string, string) {
	if maxRunes <= 0 {
		maxRunes = 48000
	}
	s := sanitizedCanonical
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, CompressionNone
	}
	head := maxRunes * 3 / 4
	tail := maxRunes / 4
	if head < 512 {
		head = 512
	}
	if tail < 256 {
		tail = 256
	}
	if head+tail >= len(runes) {
		return s, CompressionNone
	}
	out := string(runes[:head]) + "\n\n[... truncated for audit storage ...]\n\n" + string(runes[len(runes)-tail:])
	return out, CompressionHeadTail
}
