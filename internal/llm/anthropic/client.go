// Package anthropic provides an Anthropic-backed implementation of model.ChatCompleter using the Messages API.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

const defaultAnthropicBaseURL = "https://api.anthropic.com"
const anthropicAPIVersion = "2023-06-01"

// Client implements model.ChatCompleter using the Anthropic Messages API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
}

// NewClient builds an Anthropic client from config. API key is taken from cfg.LLM.APIKey or cfg.LLM.APIKeyFromEnv.
func NewClient(cfg *config.Config) (*Client, error) {
	return NewClientWithModel(cfg, cfg.LLM.Model)
}

// NewClientWithModel is like NewClient but uses modelOverride for completions. If modelOverride is empty, cfg.LLM.Model is used.
func NewClientWithModel(cfg *config.Config, modelOverride string) (*Client, error) {
	return NewClientWithKeyAndModel(cfg, "", modelOverride)
}

// NewClientWithKeyAndModel is like NewClient but uses keyOverride and modelOverride. If keyOverride is empty, cfg.LLM.APIKey/APIKeyFromEnv are used. BaseURL is only taken from cfg when cfg.LLM.Provider is anthropic (so a different provider's base_url is not used).
func NewClientWithKeyAndModel(cfg *config.Config, keyOverride, modelOverride string) (*Client, error) {
	key := keyOverride
	if key == "" {
		key = cfg.LLM.APIKey
		if cfg.LLM.APIKeyFromEnv != "" {
			key = os.Getenv(cfg.LLM.APIKeyFromEnv)
		}
	}
	if key == "" {
		return nil, fmt.Errorf("anthropic: API key required (llm.api_key or %s)", cfg.LLM.APIKeyFromEnv)
	}
	baseURL := defaultAnthropicBaseURL
	if strings.ToLower(strings.TrimSpace(cfg.LLM.Provider)) == "anthropic" && strings.TrimSpace(cfg.LLM.BaseURL) != "" {
		baseURL = strings.TrimSuffix(cfg.LLM.BaseURL, "/")
	}
	modelID := strings.TrimSpace(modelOverride)
	if modelID == "" {
		modelID = cfg.LLM.Model
	}
	if modelID == "" {
		modelID = "claude-sonnet-4-20250514"
	}
	return &Client{
		httpClient: &http.Client{},
		baseURL:    baseURL,
		apiKey:     key,
		model:      modelID,
	}, nil
}

// request body for POST /v1/messages
type messagesRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []anthropicMsg `json:"messages"`
}

type anthropicMsg struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// response from POST /v1/messages
type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete implements model.ChatCompleter.
func (c *Client) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	var system string
	var apiMessages []anthropicMsg
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		content := []contentBlock{{Type: "text", Text: m.Content}}
		if role == "system" {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}
		if role == "" || role == "user" {
			role = "user"
		} else if role == "assistant" {
			role = "assistant"
		} else {
			role = "user"
		}
		apiMessages = append(apiMessages, anthropicMsg{Role: role, Content: content})
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := messagesRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  apiMessages,
	}
	if opts.Temperature != nil && *opts.Temperature > 0 {
		// Anthropic uses temperature 0-1; we pass through
		// API accepts optional "temperature" at top level - add if needed
		_ = opts.Temperature
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: %s: %s", resp.Status, string(body))
	}
	var out messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var text string
	for _, b := range out.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	result := &model.CompleteResult{Content: text}
	if out.Usage != nil {
		result.Usage = &model.Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
		}
	}
	return result, nil
}
