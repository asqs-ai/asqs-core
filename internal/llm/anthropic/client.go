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
	"github.com/asqs/asqs-core/internal/llm/httpcfg"
	"github.com/asqs/asqs-core/internal/llm/retryhttp"
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
		// httpcfg supplies the same 5-minute timeout OpenAI and Ollama use. A bare &http.Client{}
		// has no timeout at all, so a hung connection parked a gap forever.
		httpClient: httpcfg.HTTPClient(&cfg.LLM),
		baseURL:    baseURL,
		apiKey:     key,
		model:      modelID,
	}, nil
}

// request body for POST /v1/messages
type messagesRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	System      []systemBlock  `json:"system,omitempty"`
	Messages    []anthropicMsg `json:"messages"`
	Temperature *float32       `json:"temperature,omitempty"`
}

// systemBlock is one block of the system prompt. Upstream's edition carries an optional
// cache_control breakpoint here; that whole mechanism is enterprise-excluded, so this struct has
// no such field and no request from this client can ever carry a cache_control key.
type systemBlock struct {
	Type string `json:"type"` // always "text"
	Text string `json:"text"`
}

type anthropicMsg struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is a block in a message body. Anthropic models tool calls as content blocks rather
// than as a separate message role: an assistant turn carries `tool_use` blocks, and the results come
// back as `tool_result` blocks inside the NEXT USER message — there is no "tool" role on this API.
// The tool halves go on the wire when the tool-calling wave (CP41) starts populating them.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`

	// tool_use (assistant -> caller)
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result (caller -> assistant)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// MarshalJSON emits only the fields that belong to this block's type.
//
// The struct is a union of three block shapes, so a plain marshal would put `"text":""` on every
// tool block (Text has no omitempty, deliberately — a text block with empty text must still send the
// field). Switching on Type keeps text blocks byte-identical to what this client sent before tools
// existed, which is what makes the non-tool request guarantee hold.
func (b contentBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "tool_use":
		return json.Marshal(struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{b.Type, b.ID, b.Name, b.Input})
	case "tool_result":
		return json.Marshal(struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
		}{b.Type, b.ToolUseID, b.Content})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	}
}

// isToolResultOnly reports whether a message body consists solely of tool_result blocks, which is
// how the merge target for parallel tool results is recognized once tool calling lands.
func isToolResultOnly(blocks []contentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// response from POST /v1/messages
type messagesResponse struct {
	Content []contentBlock `json:"content"`
	// StopReason is "end_turn", "max_tokens", "stop_sequence" or "tool_use". "max_tokens" means the
	// text above is a partial answer, returned with HTTP 200.
	StopReason string `json:"stop_reason"`
	Usage      *struct {
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
		System:    buildSystemBlocks(system),
		Messages:  apiMessages,
	}
	// Anthropic accepts temperature in [0,1] at the top level. It used to be discarded here with a
	// literal `_ = opts.Temperature`, so a caller setting it got no error and no effect.
	if opts.Temperature != nil {
		t := *opts.Temperature
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		body.Temperature = &t
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	// Retry transient failures through the shared helper, so Anthropic has the same
	// transient-failure tolerance as OpenAI and Ollama. Previously a single 429/502/EOF failed the
	// gap outright here while the same failure was retried transparently on OpenAI.
	resp, err := retryhttp.Do(ctx, c.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("anthropic: new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		return req, nil
	}, retryhttp.Options{})
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
	stopReason := strings.ToLower(strings.TrimSpace(out.StopReason))

	// stop_reason "max_tokens" means text above is a partial answer, returned with HTTP 200.
	if model.IsLengthStopReason(stopReason) {
		trunc := &model.TruncatedCompletionError{
			Provider:  "anthropic",
			Reason:    stopReason,
			MaxTokens: maxTokens,
			Content:   text,
		}
		if out.Usage != nil {
			trunc.GotTokens = out.Usage.OutputTokens
		}
		return nil, trunc
	}

	// Extended thinking arrives as its own content block and never reaches `text`; this covers a
	// model that emits an inline <think> preamble anyway. See model.StripReasoningBlock.
	text, thought := model.StripReasoningBlock(text)
	result := &model.CompleteResult{Content: text, StopReason: stopReason, ReasoningRunes: thought}
	if out.Usage != nil {
		result.Usage = &model.Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
		}
	}
	return result, nil
}

// Capabilities implements model.CapabilityReporter.
func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{
		StructuredOutput: false,
		Temperature:      true,
		MaxTokens:        true,
		UsageReporting:   true,
		// Upstream declares PromptCaching true; core's client carries none of the cache_control
		// machinery (excluded by the enterprise seam), so declaring support would be a lie here.
		PromptCaching: false,
		// The tool fields flip with the tool-calling wave (CP41). Their eventual values: ToolCalling
		// true, StructuredWithTools false (StructuredOutput is false above, so the pair stays
		// coherent), ToolChoiceNoneWithTools true — and on this provider that last one is REQUIRED
		// for a forced final turn: a history carrying tool_use/tool_result blocks is rejected by
		// the Messages API unless the request still declares tools.
		ToolCalling:             false,
		StructuredWithTools:     false,
		ToolChoiceNoneWithTools: false,
	}
}

// buildSystemBlocks renders the system prompt as content blocks.
//
// Upstream's edition takes a second parameter that attaches a cache_control breakpoint to the last
// block; that whole prompt-caching mechanism is enterprise-excluded, and the parameter is removed
// rather than wired to a constant false — a dead parameter is how an excluded feature creeps back.
func buildSystemBlocks(system string) []systemBlock {
	if strings.TrimSpace(system) == "" {
		return nil
	}
	return []systemBlock{{Type: "text", Text: system}}
}
