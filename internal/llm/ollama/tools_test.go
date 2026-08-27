package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// rawSchema is a json.Marshaler over fixed bytes, matching how callers supply a schema.
// (Upstream defines this in its structured-format test file; core's edition of those tests
// constructs schemas differently, so the helper lives here.)
type rawSchema string

func (r rawSchema) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// toolClient returns a client pointed at a server that records the request body. numCtx > 0 sets
// llm.ollama_num_ctx, which tool use requires.
func toolClient(t *testing.T, reply string, body *[]byte, numCtx int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClientWithKeyAndModel(&config.Config{LLM: config.LLMConfig{
		Provider: "ollama", Model: "qwen2.5", BaseURL: srv.URL, OllamaNumCtx: numCtx,
	}}, "", "qwen2.5")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const okReply = `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`

// Tool use without an explicit num_ctx must fail loudly.
//
// Ollama silently drops the oldest messages past the context window — no error, no done_reason. A
// tool loop grows the stack fast, so with a default window the model loses the original task
// mid-loop and answers confidently about something else. That is indistinguishable from a model
// quality problem, which is why this is refused rather than warned about.
func TestComplete_toolsRequireExplicitNumCtx(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 0)

	_, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{Tools: []model.ToolDefinition{{Name: "t", Schema: rawSchema(`{"type":"object"}`)}}})
	if err == nil {
		t.Fatal("expected an error when tools are used without num_ctx")
	}
	if !strings.Contains(err.Error(), "ollama_num_ctx") {
		t.Errorf("error should name the config field to set: %v", err)
	}
	if !strings.Contains(err.Error(), "silently") {
		t.Errorf("error should explain why it matters: %v", err)
	}
}

// Without tools, num_ctx stays optional — this must not become a global requirement.
func TestComplete_withoutToolsNumCtxStaysOptional(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 0)
	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{}); err != nil {
		t.Fatalf("non-tool call must not require num_ctx: %v", err)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if _, present := req["tools"]; present {
		t.Error("tools sent on a request with none")
	}
}

func TestComplete_sendsToolsWithParametersSchema(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 32768)

	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{Tools: []model.ToolDefinition{{
			Name: "get_symbol", Description: "fetch", Schema: rawSchema(`{"type":"object","properties":{"fq":{"type":"string"}}}`),
		}}}); err != nil {
		t.Fatal(err)
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %+v (%s)", req.Tools, body)
	}
	if req.Tools[0].Type != "function" || req.Tools[0].Function.Name != "get_symbol" {
		t.Errorf("tool = %+v", req.Tools[0])
	}
	// Ollama follows the OpenAI shape: `parameters`, not Anthropic's `input_schema`.
	if !strings.Contains(string(req.Tools[0].Function.Parameters), `"fq"`) {
		t.Errorf("parameters not forwarded: %s", req.Tools[0].Function.Parameters)
	}
	if v, ok := req.Options["num_ctx"]; !ok || v == nil {
		t.Errorf("num_ctx must reach the request when tools are used: %v", req.Options)
	}
}

// /api/chat has no tool_choice field. Silently dropping a caller's forcing request would produce a
// turn that does not do what was asked, so it is refused instead.
func TestComplete_rejectsUnsupportedToolChoice(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 32768)
	for _, choice := range []string{model.ToolChoiceRequired, model.ToolChoiceNone, "get_symbol"} {
		_, err := c.Complete(context.Background(),
			[]model.Message{{Role: model.RoleUser, Content: "x"}},
			model.CompleteOptions{
				Tools:      []model.ToolDefinition{{Name: "get_symbol", Schema: rawSchema(`{"type":"object"}`)}},
				ToolChoice: choice,
			})
		if err == nil {
			t.Errorf("tool_choice %q should be refused, not ignored", choice)
		}
	}
	// auto is the native behaviour and must be accepted.
	if _, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}},
		model.CompleteOptions{
			Tools:      []model.ToolDefinition{{Name: "get_symbol", Schema: rawSchema(`{"type":"object"}`)}},
			ToolChoice: model.ToolChoiceAuto,
		}); err != nil {
		t.Errorf("auto must be accepted: %v", err)
	}
}

// `arguments` is a decoded object here and a JSON string on OpenAI. Both must reach callers as
// directly-unmarshalable Args, or one tool handler works on one provider and breaks on the other.
func TestComplete_decodesToolCallsWithObjectArguments(t *testing.T) {
	t.Parallel()
	var body []byte
	reply := `{"message":{"role":"assistant","content":"","tool_calls":[` +
		`{"function":{"name":"get_symbol","arguments":{"fq_name":"com.acme.A"}}},` +
		`{"function":{"name":"find_tests","arguments":{"q":"b"}}}` +
		`]},"done":true,"done_reason":"stop"}`
	c := toolClient(t, reply, &body, 32768)

	res, err := c.Complete(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(res.ToolCalls))
	}
	var got struct {
		FQName string `json:"fq_name"`
	}
	if err := json.Unmarshal(res.ToolCalls[0].Args, &got); err != nil {
		t.Fatalf("Args not directly unmarshalable: %v (%s)", err, res.ToolCalls[0].Args)
	}
	if got.FQName != "com.acme.A" {
		t.Errorf("fq_name = %q", got.FQName)
	}
	// Ollama returns no call id; one is synthesized so callers can match results back.
	if res.ToolCalls[0].ID == "" || res.ToolCalls[0].ID == res.ToolCalls[1].ID {
		t.Errorf("synthesized ids must be present and distinct: %q, %q", res.ToolCalls[0].ID, res.ToolCalls[1].ID)
	}
}

// Ollama matches results to calls by NAME — there is no tool_call_id on this API. A RoleTool
// message carries only the id, so the client must translate.
func TestComplete_toolResultCarriesToolName(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 32768)

	if _, err := c.Complete(context.Background(), []model.Message{
		{Role: model.RoleUser, Content: "x"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "ollama_0", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"A"}`)},
		}},
		{Role: model.RoleTool, ToolCallID: "ollama_0", Content: "class A {}"},
	}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(req.Messages))
	}
	asst := req.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Name != "get_symbol" {
		t.Fatalf("assistant tool_calls = %+v", asst.ToolCalls)
	}
	// Arguments must go back as an object, not as a JSON string.
	if string(asst.ToolCalls[0].Function.Arguments) != `{"fq_name":"A"}` {
		t.Errorf("arguments re-encoded wrongly: %s", asst.ToolCalls[0].Function.Arguments)
	}
	result := req.Messages[2]
	if result.Role != "tool" {
		t.Errorf("role = %q", result.Role)
	}
	if result.ToolName != "get_symbol" {
		t.Errorf("tool_name = %q; Ollama matches results to calls by name, not id", result.ToolName)
	}
}

func TestProbeToolSupport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		want     bool
		wantPath string
	}{
		{"declares tools", 200, `{"capabilities":["completion","tools"]}`, true, "/api/show"},
		{"no tools capability", 200, `{"capabilities":["completion"]}`, false, "/api/show"},
		{"missing field", 200, `{}`, false, "/api/show"},
		{"server error", 500, `boom`, false, "/api/show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotModel string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				b, _ := io.ReadAll(r.Body)
				var req map[string]string
				_ = json.Unmarshal(b, &req)
				gotModel = req["model"]
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			ok, reason, err := ProbeToolSupport(context.Background(), srv.Client(), srv.URL+"/api/chat", "qwen2.5")
			if err != nil && tc.status == 200 {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.want {
				t.Errorf("ProbeToolSupport = %v, want %v (reason %q)", ok, tc.want, reason)
			}
			if gotPath != tc.wantPath {
				t.Errorf("probed %q, want %q", gotPath, tc.wantPath)
			}
			if gotModel != "qwen2.5" {
				t.Errorf("probe sent model %q", gotModel)
			}
			if !ok && reason == "" {
				t.Error("a negative probe must explain itself so an operator can act")
			}
		})
	}
}

// The probe is the authority, and until it runs the client must not claim tool support: sending
// definitions to a template that ignores them looks like a quality regression, not a config error.
func TestCapabilities_toolCallingFollowsTheProbe(t *testing.T) {
	t.Parallel()
	var body []byte
	c := toolClient(t, okReply, &body, 32768)
	if c.Capabilities().ToolCalling {
		t.Error("an unprobed client must not declare tool calling")
	}
	c.SetToolCalling(true)
	if !c.Capabilities().ToolCalling {
		t.Error("SetToolCalling(true) not reflected")
	}
}

func TestShowEndpoint(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"http://127.0.0.1:11434/api/chat": "http://127.0.0.1:11434/api/show",
		"http://host:1234/api/chat":       "http://host:1234/api/show",
	} {
		if got := showEndpoint(in); got != want {
			t.Errorf("showEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
