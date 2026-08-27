package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai/jsonschema"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

const plainReply = `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// toolTestServer captures the outgoing request body and replies with a canned response.
func toolTestServer(t *testing.T, reply string, body *[]byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		_ = r.Body.Close()
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(&config.Config{LLM: config.LLMConfig{
		Provider: "openai", APIKey: "test-key", Model: "gpt-4o-mini", BaseURL: srv.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Adding tool support must not change the wire format of requests that use no tools.
//
// This pipeline's prompts are measured, and a silent shape change would make every existing baseline
// incomparable without anything failing. The expected body below was captured from the pre-tools
// client and compared byte-for-byte; it is pinned here so the guarantee survives future edits.
func TestComplete_nonToolRequestIsByteIdenticalToPreToolBehaviour(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	temp := float32(0.3)
	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleSystem, Content: "sys"},
		{Role: model.RoleUser, Content: "u"},
	}, model.CompleteOptions{MaxTokens: 512, Temperature: &temp}); err != nil {
		t.Fatal(err)
	}

	const want = `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"sys"},` +
		`{"role":"user","content":"u"}],"max_completion_tokens":512,"temperature":0.3}`
	if got := strings.TrimSpace(string(body)); got != want {
		t.Errorf("non-tool request body changed shape:\n got: %s\nwant: %s", got, want)
	}
}

func TestComplete_sendsToolDefinitionsAndChoice(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	schema := &jsonschema.Definition{
		Type:       jsonschema.Object,
		Properties: map[string]jsonschema.Definition{"fq_name": {Type: jsonschema.String}},
		Required:   []string{"fq_name"},
	}
	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools:      []model.ToolDefinition{{Name: "get_symbol", Description: "fetch a symbol", Schema: schema}},
			ToolChoice: model.ToolChoiceAuto,
		}); err != nil {
		t.Fatal(err)
	}

	var req struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d: %s", len(req.Tools), body)
	}
	if req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "get_symbol" {
		t.Errorf("tool = %+v", req.Tools[0])
	}
	if !strings.Contains(string(req.Tools[0].Function.Parameters), "fq_name") {
		t.Errorf("schema not forwarded: %s", req.Tools[0].Function.Parameters)
	}
	if got := strings.Trim(string(req.ToolChoice), `"`); got != "auto" {
		t.Errorf("tool_choice = %s", req.ToolChoice)
	}
}

// A ToolChoice that is not a reserved word names a specific tool, which OpenAI expects as an object.
func TestComplete_namedToolChoiceIsAnObject(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools:      []model.ToolDefinition{{Name: "get_symbol", Schema: &jsonschema.Definition{Type: jsonschema.Object}}},
			ToolChoice: "get_symbol",
		}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ToolChoice struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if req.ToolChoice.Type != "function" || req.ToolChoice.Function.Name != "get_symbol" {
		t.Errorf("tool_choice = %+v; want a function object naming get_symbol", req.ToolChoice)
	}
}

// Parallel calls are the normal case, not an edge case: the model asks for several lookups in one
// turn. Order and ids must survive so results can be matched back to calls.
func TestComplete_decodesParallelToolCalls(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[` +
		`{"id":"call_a","type":"function","function":{"name":"get_symbol","arguments":"{\"fq_name\":\"com.acme.A\"}"}},` +
		`{"id":"call_b","type":"function","function":{"name":"find_tests","arguments":"{\"q\":\"b\"}"}}` +
		`]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	c := toolTestServer(t, reply, &body)

	res, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].ID != "call_a" || res.ToolCalls[0].Name != "get_symbol" {
		t.Errorf("call 0 = %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].ID != "call_b" || res.ToolCalls[1].Name != "find_tests" {
		t.Errorf("call 1 = %+v", res.ToolCalls[1])
	}
	// Args must be usable directly — OpenAI's `arguments` is a JSON string and needs unquoting.
	var got struct {
		FQName string `json:"fq_name"`
	}
	if err := json.Unmarshal(res.ToolCalls[0].Args, &got); err != nil {
		t.Fatalf("Args is not directly unmarshalable: %v (%s)", err, res.ToolCalls[0].Args)
	}
	if got.FQName != "com.acme.A" {
		t.Errorf("fq_name = %q", got.FQName)
	}
	if res.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

// A zero-argument tool call is legitimate and must not force callers to nil-check.
func TestComplete_emptyArgumentsBecomeEmptyObject(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
		`{"id":"c1","type":"function","function":{"name":"list_repos","arguments":""}}` +
		`]},"finish_reason":"tool_calls"}]}`
	c := toolTestServer(t, reply, &body)

	res, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || string(res.ToolCalls[0].Args) != "{}" {
		t.Fatalf("Args = %q; want {}", res.ToolCalls[0].Args)
	}
}

// Malformed arguments must fail attributably rather than being forwarded as garbage that a caller
// then fails to unmarshal somewhere far from the cause.
func TestComplete_malformedArgumentsAreAnError(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
		`{"id":"c1","type":"function","function":{"name":"get_symbol","arguments":"{not json"}}` +
		`]},"finish_reason":"tool_calls"}]}`
	c := toolTestServer(t, reply, &body)

	_, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	if err == nil {
		t.Fatal("expected an error for unparsable tool arguments")
	}
	var argsErr *model.ToolCallArgsError
	if !errors.As(err, &argsErr) {
		t.Fatalf("want *model.ToolCallArgsError, got %T: %v", err, err)
	}
	if argsErr.Tool != "get_symbol" || argsErr.Provider != "openai" {
		t.Errorf("error does not identify the call: %+v", argsErr)
	}
	if !strings.Contains(argsErr.Error(), "{not json") {
		t.Errorf("error should carry the raw arguments for debugging: %v", argsErr)
	}
}

// The transcript a tool loop sends back: an assistant turn carrying calls, then one tool message per
// call. Both must survive the round trip onto the wire or the provider rejects the turn.
func TestComplete_sendsToolResultTranscript(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "x"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "call_a", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"A"}`)},
		}},
		{Role: model.RoleTool, ToolCallID: "call_a", Content: "public class A {}"},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}

	var req struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %s", len(req.Messages), body)
	}
	asst := req.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_a" {
		t.Fatalf("assistant tool_calls not sent: %+v", asst)
	}
	// OpenAI expects arguments as a JSON *string*, so the RawMessage is re-encoded that way.
	if asst.ToolCalls[0].Function.Arguments != `{"fq_name":"A"}` {
		t.Errorf("arguments = %q", asst.ToolCalls[0].Function.Arguments)
	}
	if req.Messages[2].Role != "tool" || req.Messages[2].ToolCallID != "call_a" {
		t.Errorf("tool result message = %+v", req.Messages[2])
	}
}

// An empty tool name would be rejected by the API with an opaque error; catch it at the boundary.
func TestComplete_rejectsToolWithEmptyName(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolTestServer(t, plainReply, &body)

	_, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{Tools: []model.ToolDefinition{{Name: "  ", Schema: &jsonschema.Definition{Type: jsonschema.Object}}}})
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("want an empty-name error, got %v", err)
	}
}

// Capabilities must advertise tool calling now that it works, or callers that gate on it (B18's
// resolution order: native → prompted → one-shot) will skip the native path on this provider.
func TestCapabilities_declaresToolCalling(t *testing.T) {
	t.Parallel()
	c, err := NewClient(&config.Config{LLM: config.LLMConfig{
		Provider: "openai", APIKey: "k", Model: "gpt-4o-mini", BaseURL: "http://127.0.0.1:1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Capabilities().ToolCalling {
		t.Error("OpenAI implements tool calling but does not declare it")
	}
}
