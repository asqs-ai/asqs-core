package config

// IndexerChunkYAML configures symbol chunking for the indexer (Phases A–D).
// Nested under `indexer.chunk` in YAML. Omitted fields keep defaults from
// indexer.DefaultChunkConfig() after merge at runtime.
type IndexerChunkYAML struct {
	// MinTokens / MaxTokens approximate per-chunk size (defaults 300 / 800). 0 = use default.
	MinTokens int `yaml:"min_tokens"`
	MaxTokens int `yaml:"max_tokens"`
	// CharsPerToken approximates tokens from rune length (default 4). 0 = use default.
	CharsPerToken int `yaml:"chars_per_token"`

	// EnrichChunkContent prepends a machine-readable header before embed (Phase A). Nil = default true.
	EnrichChunkContent *bool `yaml:"enrich_chunk_content"`
	// MaxChunkHeaderRunes caps header size (default 512). 0 = use default.
	MaxChunkHeaderRunes int `yaml:"max_chunk_header_runes"`

	// EnableSecondaryChunks adds route manifest + Angular template file chunks when repo root is set (Phase C).
	EnableSecondaryChunks *bool `yaml:"enable_secondary_chunks"`
	// MergeSmallSymbols merges adjacent tiny nest_guard/dto/… symbols (Phase D). Nil = default true.
	MergeSmallSymbols *bool `yaml:"merge_small_symbols"`
}
