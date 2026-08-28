package indexer

import "testing"

func TestDefaultChunkConfig(t *testing.T) {
	cfg := DefaultChunkConfig()
	if cfg.MinTokens != 300 {
		t.Errorf("MinTokens = %d; want 300", cfg.MinTokens)
	}
	if cfg.MaxTokens != 800 {
		t.Errorf("MaxTokens = %d; want 800", cfg.MaxTokens)
	}
	if cfg.CharsPerToken != 4 {
		t.Errorf("CharsPerToken = %d; want 4", cfg.CharsPerToken)
	}
	if !cfg.EnrichChunkContent {
		t.Error("EnrichChunkContent = false; want true (default)")
	}
	if cfg.MaxChunkHeaderRunes != 512 {
		t.Errorf("MaxChunkHeaderRunes = %d; want 512", cfg.MaxChunkHeaderRunes)
	}
	if cfg.EnableSecondaryChunks {
		t.Error("EnableSecondaryChunks = true; want false (default)")
	}
	if !cfg.MergeSmallSymbols {
		t.Error("MergeSmallSymbols = false; want true (default)")
	}
}

func TestChunkConfig_ApproxTokens(t *testing.T) {
	cfg := DefaultChunkConfig()
	// 16 chars with CharsPerToken=4 -> 4 tokens
	if n := cfg.ApproxTokens("1234567890123456"); n != 4 {
		t.Errorf("ApproxTokens(16 chars) = %d; want 4", n)
	}
	// 8 chars -> 2 tokens
	if n := cfg.ApproxTokens("12345678"); n != 2 {
		t.Errorf("ApproxTokens(8 chars) = %d; want 2", n)
	}
	// empty -> 0
	if n := cfg.ApproxTokens(""); n != 0 {
		t.Errorf("ApproxTokens(empty) = %d; want 0", n)
	}
}

func TestChunkConfig_ApproxTokens_zeroCharsPerToken(t *testing.T) {
	cfg := ChunkConfig{MinTokens: 1, MaxTokens: 100, CharsPerToken: 0}
	// Should fall back to 4
	if n := cfg.ApproxTokens("aaaaaaaaaaaa"); n != 3 {
		t.Errorf("ApproxTokens with CharsPerToken=0 (fallback 4): got %d; want 3", n)
	}
}

func TestChunkConfig_ApproxTokens_unicode(t *testing.T) {
	cfg := DefaultChunkConfig()
	// 4 runes (e.g. 4 UTF-8 bytes for ASCII, or more for multi-byte)
	s := "日本語"
	if n := cfg.ApproxTokens(s); n != 1 {
		t.Errorf("ApproxTokens(%q) = %d; want 1 (3 runes / 4)", s, n)
	}
}
