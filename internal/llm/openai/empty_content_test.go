package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// wireMessages decodes the `messages` array exactly as it left the client, keeping `content`
// absent when the marshaller dropped it — which is the whole point of these tests.
func wireMessages(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req.Messages
}

// The transport-level net for the 400 that killed run api-47cdc4dce89eebc4cf55208c8c3b714f:
// `json:"content,omitempty"` drops an empty Content, the API reads the missing key as null, and
// the request is rejected with `Invalid value for 'content': expected a string, got null`.
func TestComplete_emptyToolContentIsSentAsAString(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "u"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "get_symbol"}}},
		{Role: model.RoleTool, ToolCallID: "c1", Content: ""},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}

	msgs := wireMessages(t, body)
	var seen bool
	for _, m := range msgs {
		if string(m["role"]) != `"tool"` {
			continue
		}
		seen = true
		raw, ok := m["content"]
		if !ok {
			t.Fatal("tool message went out with no content key — the API reads this as null")
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("content is not a JSON string: %s", raw)
		}
		if s == "" {
			t.Fatal("tool message content is an empty string")
		}
	}
	if !seen {
		t.Fatal("no tool message on the wire")
	}
}

// The one legal absence: an assistant turn that only asked for tools. Filling it in would change
// the request shape for every tool-using turn the pipeline makes.
func TestComplete_assistantWithToolCallsKeepsAbsentContent(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "u"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "get_symbol"}}},
		{Role: model.RoleTool, ToolCallID: "c1", Content: "body"},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}

	for _, m := range wireMessages(t, body) {
		if string(m["role"]) != `"assistant"` {
			continue
		}
		if _, ok := m["content"]; ok {
			t.Errorf("assistant tool-call turn gained a content field: %s", m["content"])
		}
	}
}

// An assistant message with neither content nor tool_calls still needs a string.
func TestContentOrPlaceholder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		content      string
		role         string
		hasToolCalls bool
		want         string
	}{
		{"passes through", "body", "tool", false, "body"},
		{"empty tool", "", "tool", false, emptyContentPlaceholder},
		{"empty user", "", "user", false, emptyContentPlaceholder},
		{"assistant with calls", "", "assistant", true, ""},
		{"assistant without calls", "", "assistant", false, emptyContentPlaceholder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentOrPlaceholder(tc.content, tc.role, tc.hasToolCalls); got != tc.want {
				t.Errorf("contentOrPlaceholder(%q, %q, %v) = %q, want %q", tc.content, tc.role, tc.hasToolCalls, got, tc.want)
			}
		})
	}
}
