// Package model holds LLM provider-agnostic interfaces and types for generation
// (unit tests, docs) and for embeddings (RAG). Implementations are in internal/llm/*.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Chat roles. RoleTool is the message a caller sends back carrying a tool's result; it must set
// ToolCallID to the ID of the ToolCall it answers.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is a single chat message.
type Message struct {
	Role    string // RoleSystem, RoleUser, RoleAssistant, RoleTool
	Content string
	// ToolCalls are the calls an assistant message requested. A turn may contain several — providers
	// emit parallel calls — so this is a slice rather than a single value, and the transcript for a
	// tool-using turn is: assistant(ToolCalls=[a,b]) → tool(ToolCallID=a) → tool(ToolCallID=b).
	ToolCalls []ToolCall
	// ToolCallID identifies which ToolCall a RoleTool message answers. Required on RoleTool, ignored
	// otherwise.
	ToolCallID string
}

// ToolDefinition describes one tool the model may call.
//
// Schema is the JSON Schema for the tool's arguments, as a json.Marshaler for the same reason
// StructuredJSONSchema.Schema is: callers build it once and providers embed it without the contract
// depending on any particular schema library.
type ToolDefinition struct {
	Name        string
	Description string
	Schema      json.Marshaler
}

// ToolCall is one call the model requested.
//
// Args is json.RawMessage, not string, deliberately. Providers disagree on the wire type — OpenAI
// sends `arguments` as a JSON *string* that must be unquoted before it parses, while Ollama sends a
// decoded JSON *object*. Normalizing at the provider boundary means callers unmarshal Args the same
// way everywhere instead of learning which provider they are talking to.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// CompleteOptions optional parameters for chat completion.
type CompleteOptions struct {
	MaxTokens   int      // 0 = provider default
	Temperature *float32 // nil = provider default
	// Structured when non-nil, requests schema-constrained JSON in the assistant message (provider-specific). Nil = free-form text.
	Structured *StructuredJSONSchema
	// Tools are the tools the model may call this turn. Nil or empty means no tool fields are sent
	// at all, so a non-tool request is byte-identical to one built before tools existed.
	Tools []ToolDefinition
	// ToolChoice is ToolChoiceAuto / ToolChoiceNone / ToolChoiceRequired, or a specific tool name to
	// force. Empty means the provider default. Ignored when Tools is empty.
	ToolChoice string
}

// WarningPromptTruncatedPrefix marks a CompleteResult warning that the prompt exceeded the
// provider's context window and was silently truncated at the FRONT — taking the system prompt,
// the output contract, and any tool definitions with it.
//
// Warnings are human-readable text, but this one has to be machine-matchable: a caller that knows
// the front of its prompt was dropped can rebuild at tighter limits, while one that only logs the
// text repeats the same oversized request forever. Observed live upstream: three test-step fix
// rounds sent ~136k-rune prompts into a 32768-token window; every round lost its tool definitions
// and its output contract to the truncation, made zero tool calls, and the run ended
// fixer_response_unusable — with the warning generated each time and discarded unread.
const WarningPromptTruncatedPrefix = "prompt_truncated: "

// IsPromptTruncatedWarning reports whether w is a front-truncation warning.
func IsPromptTruncatedWarning(w string) bool {
	return strings.HasPrefix(w, WarningPromptTruncatedPrefix)
}

// ToolChoice values for CompleteOptions.ToolChoice. Empty means the provider default.
const (
	// ToolChoiceAuto lets the model decide whether to call a tool.
	ToolChoiceAuto = "auto"
	// ToolChoiceNone forbids tool calls for this turn while still declaring the tools, which is how
	// a final "now answer" turn is requested without rebuilding the request.
	ToolChoiceNone = "none"
	// ToolChoiceRequired forces at least one tool call.
	ToolChoiceRequired = "required"
)

// ToolCallArgsError reports a tool call whose arguments are not valid JSON.
//
// It is an error rather than a best-effort passthrough because such a call cannot be executed: the
// caller would have to guess what the model meant. Surfacing it at the provider boundary keeps the
// failure attributable, and carrying the raw text makes it debuggable. A tool loop may choose to
// re-prompt on this rather than abort.
type ToolCallArgsError struct {
	Provider string
	Tool     string
	Index    int
	Raw      string
	Err      error
}

func (e *ToolCallArgsError) Error() string {
	return fmt.Sprintf("%s: tool call %d (%s) has arguments that are not valid JSON: %v (raw: %q)",
		e.Provider, e.Index, e.Tool, e.Err, e.Raw)
}

func (e *ToolCallArgsError) Unwrap() error { return e.Err }

// NormalizeToolArgs converts a provider's raw arguments payload into canonical JSON.
//
// Empty arguments become `{}` so callers can always unmarshal into a struct — a zero-argument tool
// call is legitimate and should not force every caller to nil-check. Anything else must parse as
// JSON; invalid input is an error, never silently forwarded.
func NormalizeToolArgs(provider, tool string, index int, raw string) (json.RawMessage, error) {
	t := strings.TrimSpace(raw)
	if t == "" || t == "null" {
		return json.RawMessage(`{}`), nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(t)); err != nil {
		return nil, &ToolCallArgsError{Provider: provider, Tool: tool, Index: index, Raw: raw, Err: err}
	}
	return json.RawMessage(buf.Bytes()), nil
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
	// ToolCalls are the calls the model requested this turn, in the order the provider returned
	// them. Non-empty means the caller must execute them and send RoleTool messages back before the
	// model can finish; Content is usually empty in that case.
	ToolCalls []ToolCall
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
