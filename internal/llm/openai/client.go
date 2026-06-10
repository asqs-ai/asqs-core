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
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    role,
			Content: sanitizeChatMessageContent(m.Content),
		})
	}

	req := openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: msgs,
	}
	// Use MaxCompletionTokens (required for o1, gpt-5.x, etc.); do not set deprecated MaxTokens.
	if opts.MaxTokens > 0 {
		req.MaxCompletionTokens = opts.MaxTokens
	}
	if opts.Temperature != nil {
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

	out := &model.CompleteResult{
		Content: resp.Choices[0].Message.Content,
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
