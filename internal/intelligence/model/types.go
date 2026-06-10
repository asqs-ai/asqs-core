// Package model holds LLM provider-agnostic interfaces and types for generation
// (unit tests, docs) and for embeddings (RAG). Implementations are in internal/llm/*.
package model

import (
	"context"
	"encoding/json"
)

// StructuredJSONSchema asks the provider to constrain the assistant message to valid JSON matching this schema when the provider supports it (e.g. OpenAI Chat Completions response_format json_schema). Providers that do not implement structured output ignore this field.
type StructuredJSONSchema struct {
	Name        string
	Description string
	Strict      bool
	// Schema must marshal to a JSON Schema object accepted by the provider (subset of JSON Schema / OpenAI structured outputs).
	Schema json.Marshaler
}

// Message is a single chat message (system, user, or assistant).
type Message struct {
	Role    string // "system", "user", "assistant"
	Content string
}

// CompleteOptions optional parameters for chat completion.
type CompleteOptions struct {
	MaxTokens   int      // 0 = provider default
	Temperature *float32 // nil = provider default
	// Structured when non-nil, requests schema-constrained JSON in the assistant message (provider-specific). Nil = free-form text.
	Structured *StructuredJSONSchema
}

// CompleteResult is the response from a chat completion.
type CompleteResult struct {
	Content string // assistant message text
	Usage   *Usage // token usage if reported
}

// Usage holds token usage reported by the provider.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ChatCompleter performs chat completions (for test generation, docs, future: architecture, security).
// Implementations: OpenAI, Anthropic, Google, etc.
type ChatCompleter interface {
	Complete(ctx context.Context, messages []Message, opts CompleteOptions) (*CompleteResult, error)
}

// Embedder produces embedding vectors for a batch of texts (e.g. for RAG/chunk search).
// Same role as indexer.Embedder; implementations can satisfy both.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
