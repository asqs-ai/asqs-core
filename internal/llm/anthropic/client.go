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
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	System      []systemBlock   `json:"system,omitempty"`
	Messages    []anthropicMsg  `json:"messages"`
	Temperature *float32        `json:"temperature,omitempty"`
	Tools       []anthropicTool `json:"tools,omitempty"`
	ToolChoice  *toolChoice     `json:"tool_choice,omitempty"`
}

// anthropicTool is one tool definition. The schema field is `input_schema` here, not `parameters`
// as on OpenAI.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// toolChoice mirrors Anthropic's object form. Note "any" is this API's word for "you must call
// something", where OpenAI says "required".
type toolChoice struct {
	Type string `json:"type"`           // "auto" | "any" | "tool" | "none"
	Name string `json:"name,omitempty"` // set only when Type == "tool"
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
// how the merge target for parallel tool results is recognized.
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
		if role == "system" {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}

		// A tool result is a `tool_result` block in a USER message, not its own role.
		//
		// Anthropic additionally requires every result for one assistant turn to sit in a SINGLE
		// user message: when the model makes three parallel calls, three separate user messages are
		// rejected. So consecutive RoleTool messages are merged into the message already being
		// built rather than appended one by one.
		if role == model.RoleTool {
			block := contentBlock{Type: "tool_result", ToolUseID: strings.TrimSpace(m.ToolCallID), Content: m.Content}
			if n := len(apiMessages); n > 0 && apiMessages[n-1].Role == "user" && isToolResultOnly(apiMessages[n-1].Content) {
				apiMessages[n-1].Content = append(apiMessages[n-1].Content, block)
				continue
			}
			apiMessages = append(apiMessages, anthropicMsg{Role: "user", Content: []contentBlock{block}})
			continue
		}

		if role == "assistant" {
			role = "assistant"
		} else {
			role = "user"
		}

		var content []contentBlock
		// An assistant turn that called tools may carry no text at all; emitting an empty text block
		// alongside tool_use is rejected, so only add one when there is something to say.
		if m.Content != "" || len(m.ToolCalls) == 0 {
			content = append(content, contentBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			input := tc.Args
			if len(strings.TrimSpace(string(input))) == 0 {
				input = json.RawMessage(`{}`)
			}
			content = append(content, contentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
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
	// Tools are appended AFTER the system blocks in the cacheable prefix order Anthropic uses
	// (tools, then system, then messages); stable per run, so they sit ahead of the system prompt.
	if len(opts.Tools) > 0 {
		body.Tools = make([]anthropicTool, 0, len(opts.Tools))
		for _, t := range opts.Tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				return nil, fmt.Errorf("anthropic: tool definition with empty name")
			}
			schema := json.RawMessage(`{"type":"object"}`)
			if t.Schema != nil {
				raw, err := t.Schema.MarshalJSON()
				if err != nil {
					return nil, fmt.Errorf("anthropic: tool %s schema: %w", name, err)
				}
				schema = raw
			}
			body.Tools = append(body.Tools, anthropicTool{
				Name:        name,
				Description: strings.TrimSpace(t.Description),
				InputSchema: schema,
			})
		}
		switch tc := strings.TrimSpace(opts.ToolChoice); tc {
		case "":
			// provider default
		case model.ToolChoiceAuto:
			body.ToolChoice = &toolChoice{Type: "auto"}
		case model.ToolChoiceNone:
			body.ToolChoice = &toolChoice{Type: "none"}
		case model.ToolChoiceRequired:
			// Anthropic spells "you must call something" as "any".
			body.ToolChoice = &toolChoice{Type: "any"}
		default:
			body.ToolChoice = &toolChoice{Type: "tool", Name: tc}
		}
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
	var toolCalls []model.ToolCall
	for i, b := range out.Content {
		switch b.Type {
		case "text":
			text += b.Text
		case "tool_use":
			// `input` arrives as a decoded JSON object here, not as the JSON *string* OpenAI sends.
			// Normalizing both through the same helper is what lets callers treat Args identically.
			args, err := model.NormalizeToolArgs("anthropic", b.Name, i, string(b.Input))
			if err != nil {
				return nil, err
			}
			toolCalls = append(toolCalls, model.ToolCall{ID: b.ID, Name: b.Name, Args: args})
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
	result := &model.CompleteResult{Content: text, StopReason: stopReason, ToolCalls: toolCalls, ReasoningRunes: thought}
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
//
// StructuredOutput is false and that is load-bearing: LLMGenerator sets opts.Structured AND appends
// a JSON-shape system suffix, and IsStructuredOutputAPIError never fires here because no error is
// returned — the path works only because the model tends to follow the prose instruction and
// ParsePathContentMap is forgiving. Declaring false makes callers degrade explicitly instead of
// depending on model compliance where a schema was requested. (Anthropic does support structured
// output via tool use; this client does not implement it.)
func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{
		StructuredOutput: false,
		Temperature:      true,
		MaxTokens:        true,
		UsageReporting:   true,
		// Upstream declares PromptCaching true; core's client carries none of the cache_control
		// machinery (excluded by the enterprise seam), so declaring support would be a lie here.
		PromptCaching: false,
		ToolCalling:   true,
		// StructuredOutput is false above, so this can never matter; false keeps the pair coherent.
		StructuredWithTools: false,
		// tool_choice {"type": "none"} is supported and REQUIRED for a forced final turn here: a
		// history carrying tool_use/tool_result blocks is rejected by the Messages API unless the
		// request still declares tools, so "withhold the tools field" is not an option on this
		// provider.
		ToolChoiceNoneWithTools: true,
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
