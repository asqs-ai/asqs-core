package indexer

// ChunkConfig controls how source is split into chunks (symbol-boundary, token budget).
type ChunkConfig struct {
	// MinTokens and MaxTokens define the target range per chunk (average 300–800).
	MinTokens int
	MaxTokens int
	// CharsPerToken is used to approximate token count from rune length (e.g. 4 for code).
	CharsPerToken int

	// EnrichChunkContent prepends a short machine-readable header before embed (Phase A).
	EnrichChunkContent bool
	// MaxChunkHeaderRunes caps header size (0 = default 512).
	MaxChunkHeaderRunes int

	// EnableSecondaryChunks adds route manifest + Angular template file chunks when repoRoot is set (Phase C).
	EnableSecondaryChunks bool

	// MergeSmallSymbols merges adjacent tiny nest_guard/dto/… symbols up to MinTokens (Phase D).
	MergeSmallSymbols bool
}

// DefaultChunkConfig returns a config targeting ~300–800 tokens per chunk.
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		MinTokens:             300,
		MaxTokens:             800,
		CharsPerToken:         4,
		EnrichChunkContent:    true,
		MaxChunkHeaderRunes:   512,
		EnableSecondaryChunks: false,
		MergeSmallSymbols:     true,
	}
}

// ApproxTokens returns an approximate token count for the given content.
func (c ChunkConfig) ApproxTokens(content string) int {
	if c.CharsPerToken <= 0 {
		c.CharsPerToken = 4
	}
	n := len([]rune(content))
	return (n + c.CharsPerToken - 1) / c.CharsPerToken
}
