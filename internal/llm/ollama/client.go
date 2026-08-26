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
	// DoneReason is "stop" for a natural finish, "length" when the output cap was hit.
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
		stopReason := strings.ToLower(strings.TrimSpace(out.DoneReason))
		if model.IsLengthStopReason(stopReason) {
			// MaxTokens is 0, not opts.MaxTokens: this client does not yet send opts.MaxTokens as
			// num_predict (that lands with the capability contract), so the cap that was hit is the
			// server's own default — reporting a number that was never sent would be a lie in the
			// audit trail.
			return nil, &model.TruncatedCompletionError{
				Provider:  "ollama",
				Reason:    stopReason,
				MaxTokens: 0,
				GotTokens: out.EvalCount,
				Content:   out.Message.Content,
			}
		}
		// A reasoning model puts its chain of thought in Content ahead of the answer; no caller
		// wants it, and a plain-text contract cannot survive it. See model.StripReasoningBlock.
		content, thought := model.StripReasoningBlock(out.Message.Content)
		res := &model.CompleteResult{Content: content, StopReason: stopReason, ReasoningRunes: thought}
		if w := promptOverflowWarning(out.PromptEvalCount, c.chatOptions); w != "" {
			res.Warnings = append(res.Warnings, w)
			log.Printf("[asqs] llm ollama: %s", w)
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
