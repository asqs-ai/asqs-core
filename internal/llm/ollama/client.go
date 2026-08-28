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
	// toolCalling is the cached result of ProbeToolSupport. False until probed: an unprobed client
	// falls back to prompted tools rather than sending definitions a template will ignore.
	toolCalling bool
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// Format is Ollama's native structured-output control on POST /api/chat: either the string
	// "json" or a JSON Schema object, which the server enforces during decoding. This is NOT the
	// OpenAI-compatible `response_format` (that lives on /v1/chat/completions and is a different
	// endpoint), which is why "Ollama ignores json_schema" is true of the compat path and false
	// here.
	Format  json.RawMessage `json:"format,omitempty"`
	Options map[string]any  `json:"options,omitempty"`
	Tools   []ollamaTool    `json:"tools,omitempty"`
}

// ollamaTool mirrors Ollama's tool definition, which follows the OpenAI function shape: the schema
// field is `parameters` here, unlike Anthropic's `input_schema`.
type ollamaTool struct {
	Type     string             `json:"type"` // always "function"
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ollamaToolCall is a call on the wire. Arguments is a DECODED JSON OBJECT here — the opposite of
// OpenAI, which sends a JSON string. Both are normalized into model.ToolCall.Args so one tool
// handler works against either provider.
type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on assistant messages being replayed. Ollama has a "tool" role for results,
	// like OpenAI, but identifies them by NAME rather than by a call id — the API has no
	// tool_call_id field.
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	// ToolName names which tool a "tool" role message answers.
	ToolName string `json:"tool_name,omitempty"`
}

type chatResponse struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done bool `json:"done"`
	// DoneReason is "stop" when the model finished and "length" when it hit num_predict. Note that
	// Ollama silently drops the oldest messages when the prompt exceeds num_ctx and does NOT report
	// that here — see llm.ollama_num_ctx.
	DoneReason string `json:"done_reason"`
	// EvalCount is the number of completion tokens the server generated.
	EvalCount int `json:"eval_count"`
	// PromptEvalCount is the server's own count of prompt tokens it actually evaluated — the one
	// signal available for detecting silent front-truncation without a tokenizer.
	PromptEvalCount int `json:"prompt_eval_count"`
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

// Complete implements model.ChatCompleter (non-streaming). MaxTokens maps to options.num_predict
// and Temperature to options.temperature; Structured is sent as the native `format` grammar (see
// Capabilities).
func (c *Client) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	// Ollama matches tool results to calls by NAME, not by id: there is no tool_call_id field on
	// this API. Track the name each call id was issued under so a RoleTool message — which carries
	// only the id, because that is what OpenAI and Anthropic need — can be given its tool_name.
	nameByCallID := map[string]string{}
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				nameByCallID[tc.ID] = tc.Name
			}
		}
	}

	msgs := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		cm := chatMessage{Role: role, Content: m.Content}
		if role == model.RoleTool {
			cm.ToolName = nameByCallID[strings.TrimSpace(m.ToolCallID)]
		}
		for _, tc := range m.ToolCalls {
			var oc ollamaToolCall
			oc.Function.Name = tc.Name
			oc.Function.Arguments = tc.Args
			if len(strings.TrimSpace(string(oc.Function.Arguments))) == 0 {
				oc.Function.Arguments = json.RawMessage(`{}`)
			}
			cm.ToolCalls = append(cm.ToolCalls, oc)
		}
		msgs = append(msgs, cm)
	}
	// Per-request options are merged over the client defaults (which carry num_ctx) so a caller's
	// MaxTokens/Temperature are not lost and the client's own settings are not mutated.
	opt := make(map[string]any, len(c.chatOptions)+2)
	for k, v := range c.chatOptions {
		opt[k] = v
	}
	if opts.MaxTokens > 0 {
		// num_predict is Ollama's output cap. It was never sent, so the fixer asking for 8192 got
		// the server default instead — truncation at an unknown, unrequested limit.
		opt["num_predict"] = opts.MaxTokens
	}
	if opts.Temperature != nil {
		opt["temperature"] = *opts.Temperature
	}
	if len(opt) == 0 {
		opt = nil
	}
	payload := chatRequest{
		Model:    c.model,
		Messages: msgs,
		Stream:   false,
		Format:   structuredFormat(opts.Structured),
		Options:  opt,
	}
	if len(opts.Tools) > 0 {
		// num_ctx is a hard prerequisite for tool use, not a tuning knob.
		//
		// Ollama defaults to a small context window and silently DROPS THE OLDEST MESSAGES past it
		// — no error, no done_reason, nothing in the response to detect it from. A tool loop grows
		// the message stack fast: task, tool calls, tool results, repeat. With the default window
		// the model loses the original task description partway through and then answers
		// confidently about something else. Refusing here converts a silent wrong answer into a
		// startup-time configuration error.
		if _, ok := opt["num_ctx"]; !ok {
			return nil, fmt.Errorf("ollama: llm.ollama_num_ctx must be set when tools are enabled: " +
				"Ollama silently drops the oldest messages past the context window, and a tool loop " +
				"overflows a default window quickly — the model then loses the original task with no error")
		}
		payload.Tools = make([]ollamaTool, 0, len(opts.Tools))
		for _, t := range opts.Tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				return nil, fmt.Errorf("ollama: tool definition with empty name")
			}
			schema := json.RawMessage(`{"type":"object"}`)
			if t.Schema != nil {
				raw, err := t.Schema.MarshalJSON()
				if err != nil {
					return nil, fmt.Errorf("ollama: tool %s schema: %w", name, err)
				}
				schema = raw
			}
			payload.Tools = append(payload.Tools, ollamaTool{
				Type: "function",
				Function: ollamaToolFunction{
					Name:        name,
					Description: strings.TrimSpace(t.Description),
					Parameters:  schema,
				},
			})
		}
		// Ollama's /api/chat has no tool_choice field; the model decides. Callers that need forcing
		// must use a provider that supports it, so say so rather than silently ignoring the option.
		if tc := strings.TrimSpace(opts.ToolChoice); tc != "" && tc != model.ToolChoiceAuto {
			return nil, fmt.Errorf("ollama: tool_choice %q is not supported by /api/chat (only the "+
				"model-decides default); use %q or a provider that implements forcing", tc, model.ToolChoiceAuto)
		}
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
		stopReason := strings.ToLower(strings.TrimSpace(out.DoneReason))
		if model.IsLengthStopReason(stopReason) {
			// opts.MaxTokens is now sent as num_predict, so when the caller set one it IS the cap
			// that was hit and reporting it is truthful. When the caller set none, the server's own
			// num_predict default applied — leave MaxTokens zero rather than naming a limit ASQS
			// never requested.
			return nil, &model.TruncatedCompletionError{
				Provider:  "ollama",
				Reason:    stopReason,
				MaxTokens: opts.MaxTokens,
				GotTokens: out.EvalCount,
				Content:   out.Message.Content,
			}
		}
		// A reasoning model puts its chain of thought in Content ahead of the answer; no caller
		// wants it, and a plain-text contract cannot survive it. See model.StripReasoningBlock.
		content, thought := model.StripReasoningBlock(out.Message.Content)
		res := &model.CompleteResult{Content: content, StopReason: stopReason, ReasoningRunes: thought}
		// `arguments` arrives as a decoded JSON object here, where OpenAI sends a JSON string.
		// Normalizing both through the same helper is what lets one tool handler serve either.
		//
		// Ollama returns no call id, so one is synthesized from the position. Callers match results
		// back by id, and the outbound path translates that id to the tool_name this API expects.
		for i, tc := range out.Message.ToolCalls {
			args, err := model.NormalizeToolArgs("ollama", tc.Function.Name, i, string(tc.Function.Arguments))
			if err != nil {
				return nil, err
			}
			res.ToolCalls = append(res.ToolCalls, model.ToolCall{
				ID:   fmt.Sprintf("ollama_%d", i),
				Name: tc.Function.Name,
				Args: args,
			})
		}
		if w := promptOverflowWarning(out.PromptEvalCount, opt); w != "" {
			res.Warnings = append(res.Warnings, w)
			log.Printf("[asqs] llm ollama: %s", w)
		}
		// Mapping the server's token counts restores first_wave_metrics.llm_total_tokens and
		// tokens_to_stable on the local path, which were always 0 — the entire cost side of the
		// measurement loop was blind on a supported configuration.
		if out.PromptEvalCount > 0 || out.EvalCount > 0 {
			res.Usage = &model.Usage{
				PromptTokens:     out.PromptEvalCount,
				CompletionTokens: out.EvalCount,
				TotalTokens:      out.PromptEvalCount + out.EvalCount,
			}
		}
		return res, nil
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

// promptOverflowNearFraction is how close prompt_eval_count must come to num_ctx before the prompt
// is reported as having overflowed.
//
// Not 1.0: Ollama drops whole messages, so a truncated prompt lands just under the window rather
// than exactly on it, and an exact test would miss every real case. Not much lower either, because
// a prompt legitimately filling 90% of the window is fine and a false warning trains operators to
// ignore the real one.
const promptOverflowNearFraction = 0.95

// promptOverflowWarning reports that the prompt filled the context window and was therefore
// silently truncated, or "" when it fits or num_ctx is unknown.
//
// Ollama drops the OLDEST messages when a prompt exceeds num_ctx and says nothing about it: the
// response carries done_reason "stop" and looks entirely normal. For this system the oldest message
// is the system prompt — the output contract, the artifact path — so an overflow silently removes
// the instructions the reply is judged against, and the failure presents as "the model ignored the
// format" rather than "the model never saw it".
//
// prompt_eval_count is the server's own count of the tokens it actually evaluated, so comparing it
// to the configured num_ctx is the one signal available without a tokenizer. It only works when the
// caller set llm.ollama_num_ctx; with the server default in force there is no number to compare to
// and this stays silent rather than guessing.
func promptOverflowWarning(promptTokens int, opts map[string]any) string {
	if promptTokens <= 0 || len(opts) == 0 {
		return ""
	}
	numCtx, ok := intOption(opts["num_ctx"])
	if !ok || numCtx <= 0 {
		return ""
	}
	if float64(promptTokens) < float64(numCtx)*promptOverflowNearFraction {
		return ""
	}
	return fmt.Sprintf(
		model.WarningPromptTruncatedPrefix+
			"prompt used %d of %d context tokens (num_ctx); Ollama silently drops the oldest messages past this limit, "+
			"so the system prompt may not have reached the model. Reduce the prompt or raise llm.ollama_num_ctx.",
		promptTokens, numCtx)
}

// intOption reads an option value that may have been stored as any numeric type.
func intOption(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

// structuredFormat renders CompleteOptions.Structured as Ollama's `format` value, or nil when the
// caller asked for free-form text or supplied a schema that will not marshal.
//
// A schema that fails to marshal degrades to an unconstrained request rather than failing the call.
// The caller always has the prose instruction and a defensive parser behind this — the fixer's
// whole-file contract is stated in the system prompt regardless — so an unconstrained completion is
// the old behaviour, while a returned error would cost the round outright.
func structuredFormat(s *model.StructuredJSONSchema) json.RawMessage {
	if s == nil || s.Schema == nil {
		return nil
	}
	raw, err := s.Schema.MarshalJSON()
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	// Ollama takes the schema object itself, not OpenAI's {name, strict, schema} envelope.
	return json.RawMessage(raw)
}

// Capabilities implements model.CapabilityReporter.
//
// StructuredOutput is true: Complete sends CompleteOptions.Structured as the `format` field on
// POST /api/chat, which Ollama enforces during decoding.
//
// This was false for as long as the client did not send the field, and the cost of that gap was
// concrete: a fixer that returned one file under a renamed key, which a schema enumerating the
// in-scope path makes unrepresentable. The config that run used had turned structured output off
// with the note "Ollama ignores API-level json_schema anyway", which is true of the
// OpenAI-compatible endpoint and not of this one.
func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{
		StructuredOutput: true,
		Temperature:      true,
		MaxTokens:        true,
		UsageReporting:   true,
		PromptCaching:    false,
		ToolCalling:      c.toolCalling,
		// `format` is a grammar constraint over the whole generation: with it set, the model
		// cannot emit tool-call syntax at all. Measured upstream (qwen3-coder:30b, 2026-08-18):
		// the same lookup-requiring prompt called get_symbol on every trial without format and on
		// none with it. Tool loops must defer Structured to the final tool-free turn.
		StructuredWithTools: false,
		// /api/chat has no tool_choice field (Complete rejects a forcing value when tools are
		// declared), so a final turn cannot keep tools while forbidding calls — it must drop the
		// tools field and say "no more lookups" in the message text.
		ToolChoiceNoneWithTools: false,
	}
}
