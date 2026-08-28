package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func promptedDefs() []model.ToolDefinition {
	return []model.ToolDefinition{
		{Name: ToolGetSymbol, Description: "fetch a symbol", Schema: rawJSON(`{"type":"object","properties":{"fq_name":{"type":"string"}}}`)},
		{Name: ToolSearchCode, Description: "search code", Schema: rawJSON(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
	}
}

// The review focus: models garnish their JSON. Every one of these shapes occurs in practice, and
// the failure mode must be "no tool call this turn" — never a crash, never a partial call.
func TestParsePromptedCall_toleratesMessyOutput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		wantCall bool
		wantTool string
		wantArgs string
	}{
		{
			name:     "bare object",
			content:  `{"tool":"get_symbol","arguments":{"fq_name":"com.acme.A#b"}}`,
			wantCall: true, wantTool: ToolGetSymbol, wantArgs: `{"fq_name":"com.acme.A#b"}`,
		},
		{
			name:     "fenced with prose before and after",
			content:  "I need the signature first.\n\n```json\n{\"tool\": \"get_symbol\", \"arguments\": {\"fq_name\": \"com.acme.A#b\"}}\n```\n\nThen I will write the test.",
			wantCall: true, wantTool: ToolGetSymbol, wantArgs: `{"fq_name":"com.acme.A#b"}`,
		},
		{
			name:     "openai-style name/args spelling",
			content:  `{"name":"search_code","args":{"query":"mocks a repository"}}`,
			wantCall: true, wantTool: ToolSearchCode, wantArgs: `{"query":"mocks a repository"}`,
		},
		{
			name:     "an example object precedes the real call",
			content:  "The format is {\"tool\": \"<name>\", \"arguments\": {}}. Here is my call:\n{\"tool\":\"search_code\",\"arguments\":{\"query\":\"x\"}}",
			wantCall: true, wantTool: ToolSearchCode, wantArgs: `{"query":"x"}`,
		},
		{
			name:     "braces inside a string value do not truncate the object",
			content:  `{"tool":"search_code","arguments":{"query":"class Foo { void bar() {} }"}}`,
			wantCall: true, wantTool: ToolSearchCode, wantArgs: `{"query":"class Foo { void bar() {} }"}`,
		},
		{
			name:     "arguments omitted entirely",
			content:  `{"tool":"get_symbol"}`,
			wantCall: true, wantTool: ToolGetSymbol, wantArgs: `{}`,
		},
		// Negative cases: these must be read as "the model answered", not as a call.
		{name: "plain prose", content: "Here is the test:\n\n@Test void x() {}", wantCall: false},
		{name: "empty", content: "   ", wantCall: false},
		{name: "unknown tool name", content: `{"tool":"rm_rf","arguments":{}}`, wantCall: false},
		{name: "json that is not a tool call", content: `{"answer":"42"}`, wantCall: false},
		{name: "truncated json", content: `{"tool":"get_symbol","arguments":{`, wantCall: false},
		{name: "code containing braces but no call", content: "public void f() { if (x) { y(); } }", wantCall: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, ok := ParsePromptedCall(tc.content, promptedDefs())
			if ok != tc.wantCall {
				t.Fatalf("ok = %v, want %v (call=%+v)", ok, tc.wantCall, call)
			}
			if !tc.wantCall {
				if call != nil {
					t.Errorf("no call expected but got %+v", call)
				}
				return
			}
			if call.Name != tc.wantTool {
				t.Errorf("tool = %q, want %q", call.Name, tc.wantTool)
			}
			if string(call.Args) != tc.wantArgs {
				t.Errorf("args = %s, want %s", call.Args, tc.wantArgs)
			}
			// Args must be directly unmarshalable, exactly as in native mode.
			var probe map[string]any
			if err := json.Unmarshal(call.Args, &probe); err != nil {
				t.Errorf("args not unmarshalable: %v", err)
			}
		})
	}
}

// A hallucinated tool name must never reach the dispatcher, even when the object is otherwise
// well-formed.
func TestParsePromptedCall_rejectsUnknownToolsBeforeDispatch(t *testing.T) {
	call, ok := ParsePromptedCall(`{"tool":"delete_everything","arguments":{"path":"/"}}`, promptedDefs())
	if ok || call != nil {
		t.Fatalf("unknown tool accepted: %+v", call)
	}
}

// One call per turn: a reply containing several objects yields the first VALID one and nothing else.
func TestParsePromptedCall_returnsAtMostOneCall(t *testing.T) {
	content := `{"tool":"get_symbol","arguments":{"fq_name":"A"}}
{"tool":"search_code","arguments":{"query":"b"}}`
	call, ok := ParsePromptedCall(content, promptedDefs())
	if !ok {
		t.Fatal("expected a call")
	}
	if call.Name != ToolGetSymbol {
		t.Errorf("expected the first valid call, got %q", call.Name)
	}
}

func TestPromptedInstructions_describesEveryToolAndAsksForOne(t *testing.T) {
	out := PromptedInstructions(promptedDefs())
	for _, want := range []string{ToolGetSymbol, ToolSearchCode, "fetch a symbol", "fq_name", "ONE lookup"} {
		if !strings.Contains(out, want) {
			t.Errorf("instructions missing %q:\n%s", want, out)
		}
	}
	if PromptedInstructions(nil) != "" {
		t.Error("no tools should produce no instructions, not an empty section")
	}
}

// Instructions must be stable across calls: they sit in the system prompt, which is the cacheable
// prefix, and a map-ordered rendering would break prompt caching on every request.
func TestPromptedInstructions_areStable(t *testing.T) {
	defs := promptedDefs()
	first := PromptedInstructions(defs)
	for i := 0; i < 20; i++ {
		if got := PromptedInstructions(defs); got != first {
			t.Fatal("instructions are not deterministic; this would break prompt caching")
		}
	}
	// Order of the input must not matter either.
	reversed := []model.ToolDefinition{defs[1], defs[0]}
	if PromptedInstructions(reversed) != first {
		t.Error("instructions depend on input order; the cacheable prefix would change per call")
	}
}

func TestResolveMode(t *testing.T) {
	native := model.Capabilities{ToolCalling: true}
	none := model.Capabilities{}
	for _, tc := range []struct {
		name          string
		caps          model.Capabilities
		declared      bool
		toolsEnabled  bool
		allowPrompted bool
		want          Mode
		wantReason    bool
	}{
		{"native when declared and capable", native, true, true, true, ModeNative, false},
		{"prompted when declared incapable", none, true, true, true, ModePrompted, true},
		// Undeclared is not incapable — B08's distinction. Prompted works on anything that can
		// follow an instruction, so an unknown provider gets it rather than being written off.
		{"prompted when undeclared", none, false, true, true, ModePrompted, true},
		{"one-shot when prompted disabled and incapable", none, true, true, false, ModeOneShot, true},
		{"one-shot when tools disabled", native, true, false, true, ModeOneShot, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ResolveMode(tc.caps, tc.declared, tc.toolsEnabled, tc.allowPrompted)
			if got != tc.want {
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
			// Every downgrade must carry a reason to audit; a silent one is how "tools are enabled"
			// and "the model never called a tool" coexist unnoticed.
			if (reason != "") != tc.wantReason {
				t.Errorf("reason = %q, want non-empty=%v", reason, tc.wantReason)
			}
		})
	}
}
