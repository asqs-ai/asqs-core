package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// showEndpoint derives POST /api/show from the configured base URL, the same way chatEndpoint
// derives /api/chat.
func showEndpoint(chatEP string) string {
	return strings.TrimSuffix(chatEP, "/chat") + "/show"
}

// ProbeToolSupport reports whether the configured model's chat template supports tool calling.
//
// Tool support on Ollama is a property of the MODEL'S CHAT TEMPLATE, not of the server or of the
// model family, and it changes between tags — `qwen2.5:7b` and `qwen2.5:7b-instruct` can differ.
// Any hardcoded list of "models that support tools" is wrong the week after it is written, so this
// asks the server: POST /api/show returns a `capabilities` array containing "tools" when the
// template can emit tool calls.
//
// A probe failure is reported as (false, reason, err) rather than assumed either way. Declaring
// false costs a fallback to prompted tools (B18); declaring true wrongly means the model silently
// ignores the tool definitions and answers from the prompt alone, which looks like a quality
// regression rather than a configuration problem.
func ProbeToolSupport(ctx context.Context, httpClient *http.Client, chatEndpoint, modelID string) (bool, string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false, "no model configured", nil
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	payload, err := json.Marshal(map[string]string{"model": modelID})
	if err != nil {
		return false, "marshal probe request", err
	}
	ep := showEndpoint(chatEndpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(payload))
	if err != nil {
		return false, "build probe request", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("%s unreachable: %v", ep, err), err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("%s returned %s", ep, resp.Status), nil
	}
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "decode probe response", err
	}
	for _, c := range out.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), "tools") {
			return true, "", nil
		}
	}
	return false, fmt.Sprintf("model %s does not declare the \"tools\" capability", modelID), nil
}

// SetToolCalling records a probe result so Capabilities() can report it. Callers run
// ProbeToolSupport once at startup and wire the answer in; the client performs no hidden I/O of its
// own, so constructing a client never depends on the server being reachable.
func (c *Client) SetToolCalling(ok bool) { c.toolCalling = ok }

// ChatEndpoint exposes the resolved endpoint so a startup probe can reuse it.
func (c *Client) ChatEndpoint() string { return c.endpoint }

// ModelID exposes the resolved chat model for the same reason.
func (c *Client) ModelID() string { return c.model }

// HTTPClient exposes the configured client so the probe inherits its timeout and auth header.
func (c *Client) HTTPClient() *http.Client { return c.httpClient }
