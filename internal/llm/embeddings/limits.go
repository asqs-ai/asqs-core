package llembed

import "strings"

// modelInputTokens maps an embedding model to its input token limit.
//
// The point of this table is not precision — it is that an *unknown* model gets a conservative
// default instead of the previous flat 30 000-rune cap applied to every provider. That cap is
// ~8 000 tokens for code, which is:
//
//   - **at or over** OpenAI's 8191 limit, so it did not reliably prevent the error it existed to
//     prevent, and the binary-bisection oversize fallback fired on inputs the cap was meant to
//     handle; and
//   - **far over** the window of common local models. Ollama is called with truncate:true, so the
//     server silently drops everything past the window: a 30 000-rune chunk sent to a 512-token
//     model is embedded from roughly its first 6%, and retrieval then ranks that chunk on a vector
//     describing its import block. No error, no audit entry, no metric.
var modelInputTokens = map[string]int{
	// OpenAI
	"text-embedding-3-small": 8191,
	"text-embedding-3-large": 8191,
	"text-embedding-ada-002": 8191,
	// Ollama / local
	"nomic-embed-text":       8192,
	"mxbai-embed-large":      512,
	"all-minilm":             256,
	"snowflake-arctic-embed": 512,
	"bge-m3":                 8192,
	"bge-large":              512,
	// Cohere / Voyage
	"embed-english-v3.0":      512,
	"embed-multilingual-v3.0": 512,
	"voyage-code-2":           16000,
	"voyage-3":                32000,
}

// DefaultUnknownModelTokens is the assumed window for a model absent from the table.
//
// Deliberately small. An under-estimate costs a little context on a large-window model; an
// over-estimate silently embeds a fraction of the chunk on a small-window one, which produces what
// is effectively a random vector — it will neither be found when it should be nor excluded when it
// should not. Degrading to "chunks are small enough anyway" is the safe direction, because the
// chunker targets MaxTokens 800 (~3 200 runes) and primary chunks are already well under any limit.
const DefaultUnknownModelTokens = 512

// charsPerTokenForCode converts a token budget to a rune budget. Code averages ~3.2 characters per
// token; 3.0 with a further 10% margin keeps the converted cap under the real limit.
const charsPerTokenForCode = 3.0

// MaxInputRunes returns the input rune cap for a model. An empty or unrecognized model returns the
// conservative default.
func MaxInputRunes(provider, model string) int {
	tok := lookupModelTokens(model)
	return int(float64(tok) * charsPerTokenForCode * 0.9)
}

func lookupModelTokens(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return DefaultUnknownModelTokens
	}
	if t, ok := modelInputTokens[m]; ok {
		return t
	}
	// Ollama models carry a tag: "nomic-embed-text:v1.5".
	if i := strings.IndexByte(m, ':'); i > 0 {
		if t, ok := modelInputTokens[m[:i]]; ok {
			return t
		}
	}
	// Prefix match for versioned families ("text-embedding-3-small-v2").
	for name, t := range modelInputTokens {
		if strings.HasPrefix(m, name) {
			return t
		}
	}
	return DefaultUnknownModelTokens
}
