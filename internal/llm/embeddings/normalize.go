// Package llembed implements multi-provider embeddings via go-embeddings and shared input normalization.
package llembed

import (
	"strings"
	"unicode/utf8"
)

// MaxEmbeddingInputRunes caps each input to reduce token-limit failures (aligned with prior OpenAI embedder).
const MaxEmbeddingInputRunes = 30000

// NormalizeTexts trims inputs, replaces empty strings with a minimal token, and truncates by rune count.
func NormalizeTexts(texts []string) []string {
	inputs := make([]string, len(texts))
	for i, t := range texts {
		s := strings.TrimSpace(t)
		if s == "" {
			inputs[i] = " "
			continue
		}
		if utf8.RuneCountInString(s) > MaxEmbeddingInputRunes {
			runes := []rune(s)
			s = string(runes[:MaxEmbeddingInputRunes])
		}
		inputs[i] = s
	}
	return inputs
}
