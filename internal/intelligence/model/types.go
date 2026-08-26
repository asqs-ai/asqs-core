// Package model holds LLM provider-agnostic interfaces and types for generation
// (unit tests, docs) and for embeddings (RAG). Implementations are in internal/llm/*.
package model

import (
	"context"
	"encoding/json"
	"strings"
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

// WarningPromptTruncatedPrefix marks a CompleteResult warning that the prompt exceeded the
// provider's context window and was silently truncated at the FRONT — taking the system prompt
// and the output contract with it.
//
// Warnings are human-readable text, but this one has to be machine-matchable: a caller that knows
// the front of its prompt was dropped can rebuild at tighter limits, while one that only logs the
// text repeats the same oversized request forever. Observed live upstream: three test-step fix
// rounds sent ~136k-rune prompts into a 32768-token window; every round lost its output contract
// to the truncation, and the run ended fixer_response_unusable — with the warning generated each
// time and discarded unread.
const WarningPromptTruncatedPrefix = "prompt_truncated: "

// IsPromptTruncatedWarning reports whether w is a front-truncation warning.
func IsPromptTruncatedWarning(w string) bool {
	return strings.HasPrefix(w, WarningPromptTruncatedPrefix)
}

// CompleteResult is the response from a chat completion.
type CompleteResult struct {
	Content string // assistant message text
	Usage   *Usage // token usage if reported
	// StopReason is the provider's normalized reason the completion ended, lower-cased and
	// provider-native: OpenAI finish_reason ("stop", "length", "content_filter", …), Anthropic
	// stop_reason ("end_turn", "max_tokens", …), Ollama done_reason ("stop", "length", …).
	// Empty when the provider did not report one. A length stop is additionally surfaced as a
	// *TruncatedCompletionError so callers cannot consume a partial artifact by accident.
	StopReason string
	// ReasoningRunes is how many runes of leading chain-of-thought were removed from Content by
	// StripReasoningBlock. Zero for a model that emits none, which is every non-reasoning model.
	//
	// Reported rather than merely done. A silent transformation of a provider's reply is exactly the
	// kind of thing that becomes unexplainable later — the sibling complaint to bumpForTruncation
	// firing with no audit event — and the count is also the cheapest signal that a local endpoint
	// is fronting a reasoning model at all.
	ReasoningRunes int
	// Warnings carries provider-side conditions that did not fail the call but change how its
	// result should be read — today, an Ollama prompt that filled the model's context window and
	// was therefore silently truncated at the front. Providers that have nothing to report leave
	// it nil; callers that audit it should treat entries as human-readable, not as a stable API.
	Warnings []string
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
