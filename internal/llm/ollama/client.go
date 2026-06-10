// Package ollama implements model.ChatCompleter against the Ollama HTTP API (POST /api/chat).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
	"github.com/asqs/asqs-core/internal/llm/httpcfg"
)

const chatMaxAttempts = 5

// Client implements model.ChatCompleter for Ollama.
type Client struct {
	httpClient  *http.Client
	endpoint    string
	model       string
	chatOptions map[string]any // JSON "options" object for POST /api/chat; nil if unset
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

func chatEndpoint(cfg *config.Config) string {
	u := strings.TrimSuffix(strings.TrimSpace(cfg.LLM.BaseURL), "/")
	if u == "" {
		// IPv4 loopback: see llembed.defaultOllamaAPIRoot — avoid [::1] when Ollama is IPv4-only.
		u = "http://127.0.0.1:11434"
	}
	if strings.HasSuffix(u, "/api") {
		return u + "/chat"
	}
	return u + "/api/chat"
}

func maybeLogResolvedOllama(kind, endpoint, modelID string) {
	if s := strings.TrimSpace(strings.ToLower(os.Getenv("ASQS_LOG_RESOLVED_LLM_ENDPOINTS"))); s != "1" && s != "true" && s != "yes" {
		return
	}
	log.Printf("[asqs] llm ollama %s: url=%s model=%s", kind, endpoint, modelID)
}

// NewClientWithKeyAndModel builds an Ollama chat client. keyOverride is optional (Bearer token for gateways).
func NewClientWithKeyAndModel(cfg *config.Config, keyOverride, chatModel string) (*Client, error) {
	modelID := strings.TrimSpace(chatModel)
	if modelID == "" {
		modelID = strings.TrimSpace(cfg.LLM.Model)
	}
	if modelID == "" {
		return nil, fmt.Errorf("ollama: llm.model required")
	}
	key := strings.TrimSpace(keyOverride)
	var opts map[string]any
	if n := cfg.LLM.OllamaNumCtx; n > 0 {
		opts = map[string]any{"num_ctx": n}
	}
	ep := chatEndpoint(cfg)
	maybeLogResolvedOllama("chat", ep, modelID)
	return &Client{
		httpClient:  httpcfg.HTTPClientWithBearerForOllama(&cfg.LLM, key),
		endpoint:    ep,
		model:       modelID,
		chatOptions: opts,
	}, nil
}

// Complete implements model.ChatCompleter (non-streaming). Structured output from opts is ignored for Ollama.
func (c *Client) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	msgs := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: m.Content})
	}
	payload := chatRequest{
		Model:    c.model,
		Messages: msgs,
		Stream:   false,
		Options:  c.chatOptions,
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(&payload); err != nil {
		return nil, fmt.Errorf("ollama chat: encode request: %w", err)
	}

	// Snapshot encoded body for retries (includes options when configured).
	rawPayload := append([]byte(nil), body.Bytes()...)
	var lastErr error
	for attempt := 0; attempt < chatMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			if err := llembed.SleepBeforeRetry(ctx, attempt); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(rawPayload))
		if err != nil {
			return nil, fmt.Errorf("ollama chat: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !llembed.IsRetriableChatTransport(err) {
				return nil, fmt.Errorf("ollama chat: %w", err)
			}
			continue
		}
		respBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("ollama chat: status %d: %s", resp.StatusCode, truncate(string(respBody), 512))
			if !llembed.IsRetriableHTTPStatus(resp.StatusCode) {
				return nil, lastErr
			}
			continue
		}
		var out chatResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, fmt.Errorf("ollama chat: decode response: %w", err)
		}
		return &model.CompleteResult{Content: out.Message.Content}, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("ollama chat: %w", lastErr)
	}
	return nil, fmt.Errorf("ollama chat: exhausted retries")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
