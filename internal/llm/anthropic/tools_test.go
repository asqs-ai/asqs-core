package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

type rawSchema string

func (r rawSchema) MarshalJSON() ([]byte, error) { return []byte(r), nil }

const endTurnReply = `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":10,"output_tokens":5}}`

// captureClient records the outgoing request body and replies with a canned response.
func captureClient(t *testing.T, reply string, body *[]byte) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		_ = r.Body.Close()
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	})
}

// Text blocks must serialize exactly as before tools existed. contentBlock is now a union of three
// shapes, and a naive marshal would put `"text":""` on every tool block and reorder nothing —
// but it would also add empty tool fields to text blocks if omitempty were used carelessly.
func TestComplete_nonToolRequestKeepsTextBlockShape(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleSystem, Content: "sys"},
		{Role: model.RoleUser, Content: "u"},
	}, model.CompleteOptions{MaxTokens: 100}); err != nil {
		t.Fatal(err)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"tools", "tool_choice"} {
		if _, present := req[k]; present {
			t.Errorf("%q sent with no tools in play", k)
		}
	}
	if got := string(req["messages"]); got != `[{"role":"user","content":[{"type":"text","text":"u"}]}]` {
		t.Errorf("text block shape changed: %s", got)
	}
}

func TestComplete_sendsToolsWithInputSchema(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)

	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools: []model.ToolDefinition{{
				Name: "get_symbol", Description: "fetch", Schema: rawSchema(`{"type":"object","properties":{"fq":{"type":"string"}}}`),
			}},
			ToolChoice: model.ToolChoiceAuto,
		}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_symbol" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	// Anthropic names the schema field input_schema; sending `parameters` (the OpenAI spelling)
	// would be silently ignored and the model would receive a tool with no argument schema.
	if !strings.Contains(string(req.Tools[0].InputSchema), `"fq"`) {
		t.Errorf("input_schema missing or empty: %s", req.Tools[0].InputSchema)
	}
	if len(req.Tools[0].Parameters) != 0 {
		t.Errorf("`parameters` is the OpenAI spelling and must not be sent: %s", req.Tools[0].Parameters)
	}
	if req.ToolChoice.Type != "auto" {
		t.Errorf("tool_choice = %+v", req.ToolChoice)
	}
}

// "required" is OpenAI's word; Anthropic spells the same intent "any". Sending "required" here
// would be rejected by the API.
func TestComplete_toolChoiceRequiredMapsToAny(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)

	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools:      []model.ToolDefinition{{Name: "t", Schema: rawSchema(`{"type":"object"}`)}},
			ToolChoice: model.ToolChoiceRequired,
		}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ToolChoice struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
	}
	_ = json.Unmarshal(body, &req)
	if req.ToolChoice.Type != "any" {
		t.Errorf("tool_choice.type = %q; Anthropic spells 'required' as 'any'", req.ToolChoice.Type)
	}
}

func TestComplete_namedToolChoice(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)
	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools:      []model.ToolDefinition{{Name: "get_symbol", Schema: rawSchema(`{"type":"object"}`)}},
			ToolChoice: "get_symbol",
		}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	_ = json.Unmarshal(body, &req)
	if req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "get_symbol" {
		t.Errorf("tool_choice = %+v", req.ToolChoice)
	}
}

// `input` is a decoded JSON object on this API, not the JSON string OpenAI sends. Both must reach
// callers as directly-unmarshalable Args.
func TestComplete_decodesToolUseBlocks(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"content":[` +
		`{"type":"text","text":"let me look"},` +
		`{"type":"tool_use","id":"toolu_1","name":"get_symbol","input":{"fq_name":"com.acme.A"}},` +
		`{"type":"tool_use","id":"toolu_2","name":"find_tests","input":{"q":"b"}}` +
		`],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
	c := captureClient(t, reply, &body)

	res, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "let me look" {
		t.Errorf("text blocks must still be concatenated: %q", res.Content)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].ID != "toolu_1" || res.ToolCalls[0].Name != "get_symbol" {
		t.Errorf("call 0 = %+v", res.ToolCalls[0])
	}
	var got struct {
		FQName string `json:"fq_name"`
	}
	if err := json.Unmarshal(res.ToolCalls[0].Args, &got); err != nil {
		t.Fatalf("Args not unmarshalable: %v (%s)", err, res.ToolCalls[0].Args)
	}
	if got.FQName != "com.acme.A" {
		t.Errorf("fq_name = %q", got.FQName)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

// Anthropic has no "tool" role: results are tool_result blocks in a user message, and every result
// for one assistant turn must sit in a SINGLE user message. Three parallel calls answered by three
// separate user messages are rejected by the API.
func TestComplete_parallelToolResultsMergeIntoOneUserMessage(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "x"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "toolu_1", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"A"}`)},
			{ID: "toolu_2", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"B"}`)},
		}},
		{Role: model.RoleTool, ToolCallID: "toolu_1", Content: "class A {}"},
		{Role: model.RoleTool, ToolCallID: "toolu_2", Content: "class B {}"},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   string          `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages (user, assistant, merged tool results), got %d: %s", len(req.Messages), body)
	}

	asst := req.Messages[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 {
		t.Fatalf("assistant turn = %+v", asst)
	}
	// No text was supplied, so no empty text block may be emitted alongside tool_use.
	for _, b := range asst.Content {
		if b.Type != "tool_use" {
			t.Errorf("unexpected block %q in a text-free assistant tool turn", b.Type)
		}
	}
	if asst.Content[0].ID != "toolu_1" || string(asst.Content[0].Input) != `{"fq_name":"A"}` {
		t.Errorf("tool_use block = %+v", asst.Content[0])
	}

	results := req.Messages[2]
	if results.Role != "user" {
		t.Errorf("tool results must be a user message, got role %q", results.Role)
	}
	if len(results.Content) != 2 {
		t.Fatalf("parallel results must merge into one message; got %d block(s) across message 3", len(results.Content))
	}
	if results.Content[0].ToolUseID != "toolu_1" || results.Content[1].ToolUseID != "toolu_2" {
		t.Errorf("tool_result ids = %q, %q", results.Content[0].ToolUseID, results.Content[1].ToolUseID)
	}
	if results.Content[0].Content != "class A {}" {
		t.Errorf("tool_result content = %q", results.Content[0].Content)
	}
}

// An assistant turn that has both prose and calls must keep the text block.
func TestComplete_assistantTextAndToolUseCoexist(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)
	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "x"},
		{Role: model.RoleAssistant, Content: "checking", ToolCalls: []model.ToolCall{{ID: "t1", Name: "n", Args: json.RawMessage(`{}`)}}},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `{"type":"text","text":"checking"}`) {
		t.Errorf("assistant text dropped: %s", body)
	}
	if !strings.Contains(string(body), `"type":"tool_use"`) {
		t.Errorf("tool_use dropped: %s", body)
	}
}

// Upstream pins here that tools do not displace the cache_control breakpoint from the last system
// block. Core's client carries no cache machinery at all (the enterprise seam — see
// TestComplete_neverSendsCacheControl), so the adapted concern is the part that still exists: a
// tool-carrying request must keep its system blocks intact and must STILL never emit cache_control.
func TestComplete_toolsDoNotDisturbSystemBlocks(t *testing.T) {
	t.Parallel()
	var body []byte
	c := captureClient(t, endTurnReply, &body)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleSystem, Content: "sys prompt"},
		{Role: model.RoleUser, Content: "x"},
	}, model.CompleteOptions{
		Tools: []model.ToolDefinition{{Name: "get_symbol", Schema: rawSchema(`{"type":"object"}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools not sent: %s", body)
	}
	if len(req.System) != 1 || req.System[0].Text != "sys prompt" {
		t.Fatalf("tools disturbed the system blocks: %s", body)
	}
	if bytes.Contains(body, []byte("cache_control")) {
		t.Fatal("a tool-carrying request emitted cache_control; the cache seam leaked")
	}
}

func TestComplete_malformedToolInputIsAnError(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"content":[{"type":"tool_use","id":"t1","name":"get_symbol","input":"not-an-object-but-a-string"}],` +
		`"stop_reason":"tool_use"}`
	c := captureClient(t, reply, &body)
	res, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	// A JSON string is valid JSON, so this decodes; the point is it must not panic or silently drop.
	if err != nil {
		var argsErr *model.ToolCallArgsError
		if !errors.As(err, &argsErr) {
			t.Fatalf("unexpected error type %T: %v", err, err)
		}
		return
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool call dropped: %+v", res.ToolCalls)
	}
}

func TestCapabilities_declaresToolCalling(t *testing.T) {
	t.Parallel()
	c, err := NewClient(&config.Config{LLM: config.LLMConfig{
		Provider: "anthropic", APIKey: "k", Model: "claude-sonnet-4-20250514", BaseURL: "http://127.0.0.1:1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Capabilities().ToolCalling {
		t.Error("Anthropic implements tool calling but does not declare it")
	}
}

// contentBlock is a union of three shapes sharing one struct. Text has no omitempty — a text block
// with empty text must still send the field — so without a type-aware marshaller every tool_use and
// tool_result block would also carry `"text":""`. Anthropic rejects unknown//empty fields on tool
// blocks, and the failure would only appear against the live API.
func TestContentBlock_toolBlocksCarryNoTextField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		block contentBlock
		want  []string
		deny  []string
	}{
		{
			name:  "tool_use",
			block: contentBlock{Type: "tool_use", ID: "t1", Name: "get_symbol", Input: json.RawMessage(`{"a":1}`)},
			want:  []string{`"type":"tool_use"`, `"id":"t1"`, `"name":"get_symbol"`, `"input":{"a":1}`},
			deny:  []string{`"text"`, `"tool_use_id"`, `"content"`},
		},
		{
			name:  "tool_result",
			block: contentBlock{Type: "tool_result", ToolUseID: "t1", Content: "body"},
			want:  []string{`"type":"tool_result"`, `"tool_use_id":"t1"`, `"content":"body"`},
			deny:  []string{`"text"`, `"input"`, `"name"`},
		},
		{
			name:  "text keeps its field even when empty",
			block: contentBlock{Type: "text", Text: ""},
			want:  []string{`"type":"text"`, `"text":""`},
			deny:  []string{`"id"`, `"input"`, `"tool_use_id"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.block)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(b), w) {
					t.Errorf("missing %s in %s", w, b)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(string(b), d) {
					t.Errorf("unexpected %s in %s", d, b)
				}
			}
		})
	}
}
