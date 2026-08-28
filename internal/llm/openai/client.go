// Package openai provides an OpenAI-backed implementation of model.ChatCompleter and model.Embedder.
package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
	"github.com/asqs/asqs-core/internal/llm/httpcfg"
)

const chatCompletionMaxAttempts = 5

// Client implements model.ChatCompleter and model.Embedder using the OpenAI API.
type Client struct {
	client         *openai.Client
	model          string
	embeddingModel openai.EmbeddingModel
}

// NewClient builds an OpenAI client from config. API key is taken from cfg.LLM.APIKey
// or from the env var named in cfg.LLM.APIKeyFromEnv.
func NewClient(cfg *config.Config) (*Client, error) {
	return NewClientWithKey(cfg, "")
}

// NewClientWithKey builds an OpenAI client using keyOverride when non-empty; otherwise uses cfg.LLM.APIKey / APIKeyFromEnv.
// Used when the embedder is configured with a different provider (e.g. embedding_provider=openai) and its own key.
func NewClientWithKey(cfg *config.Config, keyOverride string) (*Client, error) {
	return NewClientWithKeyAndModel(cfg, keyOverride, cfg.LLM.Model)
}

// NewClientWithKeyAndModel is like NewClientWithKey but uses chatModel for completions (e.g. for per-step model overrides). If chatModel is empty, cfg.LLM.Model is used.
func NewClientWithKeyAndModel(cfg *config.Config, keyOverride, chatModel string) (*Client, error) {
	key := keyOverride
	if key == "" {
		key = cfg.LLM.APIKey
		if cfg.LLM.APIKeyFromEnv != "" {
			key = os.Getenv(cfg.LLM.APIKeyFromEnv)
		}
	}
	if key == "" {
		return nil, fmt.Errorf("openai: API key required (llm.api_key or %s)", cfg.LLM.APIKeyFromEnv)
	}

	openaiCfg := openai.DefaultConfig(key)
	// Only use config BaseURL when the default provider is OpenAI/Azure (so a step using a different provider doesn't get the wrong base).
	if p := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider)); (p == "openai" || p == "azure_openai") && strings.TrimSpace(cfg.LLM.BaseURL) != "" {
		openaiCfg.BaseURL = strings.TrimSpace(cfg.LLM.BaseURL)
	}
	openaiCfg.HTTPClient = httpcfg.HTTPClient(&cfg.LLM)

	modelID := chatModel
	if modelID == "" {
		modelID = cfg.LLM.Model
	}
	if modelID == "" {
		modelID = openai.GPT4o
	}
	embModel := cfg.LLM.EmbeddingModel
	if embModel == "" {
		embModel = string(openai.AdaEmbeddingV2)
	}

	return &Client{
		client:         openai.NewClientWithConfig(openaiCfg),
		model:          modelID,
		embeddingModel: openai.EmbeddingModel(embModel),
	}, nil
}

// NewClientWithKeyForEmbedding is like NewClientWithKey but applies llm.base_url whenever set.
// Use for embedding-only Azure OpenAI (and proxies) when llm.provider is not openai/azure_openai.
func NewClientWithKeyForEmbedding(cfg *config.Config, keyOverride string) (*Client, error) {
	key := keyOverride
	if key == "" {
		key = cfg.LLM.APIKey
		if cfg.LLM.APIKeyFromEnv != "" {
			key = os.Getenv(cfg.LLM.APIKeyFromEnv)
		}
	}
	if key == "" {
		return nil, fmt.Errorf("openai: API key required for embeddings (llm.embedding_api_key / llm.api_key)")
	}
	openaiCfg := openai.DefaultConfig(key)
	if u := strings.TrimSpace(cfg.LLM.BaseURL); u != "" {
		openaiCfg.BaseURL = u
	}
	openaiCfg.HTTPClient = httpcfg.HTTPClient(&cfg.LLM)

	modelID := cfg.LLM.Model
	if modelID == "" {
		modelID = openai.GPT4o
	}
	embModel := cfg.LLM.EmbeddingModel
	if embModel == "" {
		embModel = string(openai.AdaEmbeddingV2)
	}
	return &Client{
		client:         openai.NewClientWithConfig(openaiCfg),
		model:          modelID,
		embeddingModel: openai.EmbeddingModel(embModel),
	}, nil
}

// Complete implements model.ChatCompleter.
func (c *Client) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	msgs := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		role := m.Role
		if role == "" {
			role = openai.ChatMessageRoleUser
		}
		om := openai.ChatCompletionMessage{
			Role:    role,
			Content: sanitizeChatMessageContent(m.Content),
		}
		// A tool result must carry the id of the call it answers, or the API rejects the turn.
		om.ToolCallID = strings.TrimSpace(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			args := string(tc.Args)
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, openai.ToolCall{
				ID:       tc.ID,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: tc.Name, Arguments: args},
			})
		}
		om.Content = contentOrPlaceholder(om.Content, om.Role, len(om.ToolCalls) > 0)
		msgs = append(msgs, om)
	}

	req := openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: msgs,
	}
	// Use MaxCompletionTokens (required for o1, gpt-5.x, etc.); do not set deprecated MaxTokens.
	if opts.MaxTokens > 0 {
		req.MaxCompletionTokens = opts.MaxTokens
	}
	// Reasoning models fix the sampling parameters and REJECT the request outright when one is
	// sent with any other value — they do not ignore it:
	//
	//	this model has beta-limitations, temperature, top_p and n are fixed at 1,
	//	while presence_penalty and frequency_penalty are fixed at 0
	//
	// That is a 400 per call, so every gap fails and the run generates nothing.
	//
	// Omitted rather than pinned to the permitted value: 1 is already the server-side default, and
	// sending it would silently reinstate the field the day OpenAI widens what these models accept.
	if opts.Temperature != nil && !modelFixesSamplingParams(c.model) {
		req.Temperature = float32(*opts.Temperature)
	}
	if s := opts.Structured; s != nil && s.Schema != nil && strings.TrimSpace(s.Name) != "" {
		req.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:        strings.TrimSpace(s.Name),
				Description: strings.TrimSpace(s.Description),
				Schema:      s.Schema,
				Strict:      s.Strict,
			},
		}
	}
	// Tool fields are only set when tools are actually requested, so a request built without them
	// marshals exactly as it did before tools existed (omitempty on Tools/ToolChoice).
	if len(opts.Tools) > 0 {
		req.Tools = make([]openai.Tool, 0, len(opts.Tools))
		for _, t := range opts.Tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				return nil, fmt.Errorf("openai chat: tool definition with empty name")
			}
			req.Tools = append(req.Tools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        name,
					Description: strings.TrimSpace(t.Description),
					Parameters:  t.Schema,
				},
			})
		}
		if tc := strings.TrimSpace(opts.ToolChoice); tc != "" {
			switch tc {
			case model.ToolChoiceAuto, model.ToolChoiceNone, model.ToolChoiceRequired:
				req.ToolChoice = tc
			default:
				// Any other value names a specific tool to force.
				req.ToolChoice = openai.ToolChoice{
					Type:     openai.ToolTypeFunction,
					Function: openai.ToolFunction{Name: tc},
				}
			}
		}
	}

	var resp openai.ChatCompletionResponse
	var err error
	for attempt := 0; attempt < chatCompletionMaxAttempts; attempt++ {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if attempt > 0 {
			if errSleep := sleepBeforeOpenAIRetry(ctx, attempt); errSleep != nil {
				return nil, errSleep
			}
		}
		resp, err = c.client.CreateChatCompletion(ctx, req)
		if err == nil {
			break
		}
		if !isRetriableOpenAIChatError(err) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai chat: no choices in response")
	}

	choice := resp.Choices[0]
	stopReason := strings.ToLower(strings.TrimSpace(string(choice.FinishReason)))

	// finish_reason "length" means the model hit MaxCompletionTokens mid-output. The HTTP status is
	// 200 and the body parses, so without this check a half-written test class — or a JSON object
	// cut mid-string — is consumed as a finished artifact.
	if model.IsLengthStopReason(stopReason) {
		return nil, &model.TruncatedCompletionError{
			Provider:  "openai",
			Reason:    stopReason,
			MaxTokens: opts.MaxTokens,
			GotTokens: resp.Usage.CompletionTokens,
			Content:   choice.Message.Content,
		}
	}

	// An OpenAI-compatible endpoint fronting a reasoning model (Ollama, vLLM, DeepSeek's own API)
	// returns the chain of thought inline in Content. See model.StripReasoningBlock.
	content, thought := model.StripReasoningBlock(choice.Message.Content)
	out := &model.CompleteResult{
		Content:        content,
		StopReason:     stopReason,
		ReasoningRunes: thought,
	}
	// OpenAI sends `arguments` as a JSON string; normalize so callers unmarshal Args directly.
	for i, tc := range choice.Message.ToolCalls {
		args, err := model.NormalizeToolArgs("openai", tc.Function.Name, i, tc.Function.Arguments)
		if err != nil {
			return nil, err
		}
		out.ToolCalls = append(out.ToolCalls, model.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}
	if resp.Usage.TotalTokens != 0 {
		out.Usage = &model.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	return out, nil
}

// Embed implements model.Embedder.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	inputs := llembed.NormalizeTexts(texts)
	req := openai.EmbeddingRequest{
		Model: c.embeddingModel,
		Input: inputs,
	}
	var resp openai.EmbeddingResponse
	var err error
	for attempt := 0; attempt < chatCompletionMaxAttempts; attempt++ {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if attempt > 0 {
			if errSleep := sleepBeforeOpenAIRetry(ctx, attempt); errSleep != nil {
				return nil, errSleep
			}
		}
		resp, err = c.client.CreateEmbeddings(ctx, req)
		if err == nil {
			break
		}
		if !isRetriableOpenAIChatError(err) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: %w", err)
	}

	// API returns in request order; sort by index if needed for batched responses
	out := make([][]float32, len(texts))
	for _, e := range resp.Data {
		idx := e.Index
		if idx >= len(out) {
			continue
		}
		out[idx] = e.Embedding
	}
	// If any slot is missing, fill from first N in order (some providers don't set Index)
	for i := range out {
		if out[i] == nil && len(resp.Data) > 0 {
			for _, e := range resp.Data {
				out[i] = e.Embedding
				break
			}
		}
	}
	return out, nil
}

func chatCompletionRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	d := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
	if d > 8*time.Second {
		return 8 * time.Second
	}
	return d
}

// sleepBeforeOpenAIRetry waits with exponential backoff plus random jitter before retry attempt (attempt >= 1).
func sleepBeforeOpenAIRetry(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	base := chatCompletionRetryBackoff(attempt)
	jitter := time.Duration(rand.Int64N(int64(500 * time.Millisecond)))
	d := base + jitter
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// sanitizeChatMessageContent strips NULs and replaces invalid UTF-8 so the chat request body is always valid JSON.
// Compiler/test output (e.g. Maven, javac) can embed control bytes or non-UTF8 sequences; encoding/json may emit
// problematic sequences that gateways reject as "could not parse the JSON body".
func sanitizeChatMessageContent(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "\x00") && utf8.ValidString(s) {
		return s
	}
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.ToValidUTF8(s, "\uFFFD")
}

// isRetriableOpenAIChatError matches transient failures (connection drops, overload) — not auth or bad requests.
func isRetriableOpenAIChatError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "unexpected eof") || strings.Contains(s, "eof") && (strings.Contains(s, "read") || strings.Contains(s, "post") || strings.Contains(s, "http")) ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") || strings.Contains(s, "server closed") ||
		strings.Contains(s, "tls handshake") || strings.Contains(s, "read tcp") {
		return true
	}
	// Rate limit / gateway — safe to retry with backoff
	if strings.Contains(s, "429") || strings.Contains(s, "502") || strings.Contains(s, "503") || strings.Contains(s, "504") {
		return true
	}
	return false
}

// emptyContentPlaceholder stands in for a message body that reached the client empty. Its text is
// deliberately self-describing: it can surface in a transcript, and "(empty)" reads better there
// than a lone space would.
const emptyContentPlaceholder = "(empty)"

// contentOrPlaceholder keeps a message body from vanishing on the wire.
//
// go-openai marshals the field as `json:"content,omitempty"`, so an empty Content DROPS the key
// rather than sending "". The API types `content` as a string wherever it is required and reports
// the missing key as null — `Invalid value for 'content': expected a string, got null` — which is
// a 400 that kills the whole request, not just the offending message. An upstream run died exactly
// there when a tool result was blanked by the shared context budget.
//
// The one place an absent content is legal is an assistant message that carries tool_calls: the
// model asked for tools and said nothing, and OpenAI's schema allows it. Every other role needs a
// string, so an empty one becomes a placeholder.
//
// This is the transport-level net; today core never builds an empty message, but the tool loop
// will. Callers that know WHY a body is empty should say so in the body itself — reaching this
// function means nobody did.
func contentOrPlaceholder(content, role string, hasToolCalls bool) string {
	if strings.TrimSpace(content) != "" {
		return content
	}
	if role == openai.ChatMessageRoleAssistant && hasToolCalls {
		return content
	}
	return emptyContentPlaceholder
}

// modelFixesSamplingParams reports whether a model pins temperature/top_p/n/presence_penalty/
// frequency_penalty and rejects any request that sets one to another value.
//
// This is the reasoning-model family: the o-series and gpt-5.x. Matching is by prefix on the bare
// model name so a dated snapshot (o3-mini-2025-01-31), a fine-tune suffix, or an Azure deployment
// named after its model all resolve the same way. An Azure deployment given an unrelated name
// cannot be classified from the string and falls through to sending the parameter — the same
// behaviour as before this existed, and the API's error names the cause.
//
// Deliberately not a general "is a reasoning model" predicate: the only thing being decided is
// whether these five request fields are accepted.
func modelFixesSamplingParams(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	// Azure deployments are often referenced with a path-ish or suffixed name.
	if i := strings.LastIndexAny(n, "/:"); i >= 0 && i+1 < len(n) {
		n = n[i+1:]
	}
	for _, p := range []string{"o1", "o3", "o4", "gpt-5"} {
		if n == p || strings.HasPrefix(n, p+"-") || strings.HasPrefix(n, p+".") {
			return true
		}
	}
	return false
}

// Capabilities implements model.CapabilityReporter. OpenAI/Azure acts on every CompleteOptions
// field today except Temperature on a reasoning model, which pins it (see
// modelFixesSamplingParams). Prompt caching is automatic for prefixes over the provider's
// threshold and needs no directive, so it is declared true without a cache_control equivalent.
func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{
		StructuredOutput: true,
		Temperature:      !modelFixesSamplingParams(c.model),
		MaxTokens:        true,
		UsageReporting:   true,
		PromptCaching:    true,
		ToolCalling:      true,
		// response_format and tools are separate request fields; the API composes them.
		StructuredWithTools: true,
		// tool_choice "none" is legal alongside tools; the API keeps the declarations (and the
		// cacheable prefix) while forbidding calls.
		ToolChoiceNoneWithTools: true,
	}
}
