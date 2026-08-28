package model

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizeToolArgs_emptyBecomesEmptyObject(t *testing.T) {
	for _, in := range []string{"", "   ", "null", "\n\t"} {
		got, err := NormalizeToolArgs("p", "tool", 0, in)
		if err != nil {
			t.Fatalf("NormalizeToolArgs(%q): %v", in, err)
		}
		if string(got) != "{}" {
			t.Errorf("NormalizeToolArgs(%q) = %s; want {}", in, got)
		}
	}
}

// The provider boundary is where wire differences are erased. OpenAI sends a JSON string; whatever
// arrives, Args must be directly unmarshalable by the caller.
func TestNormalizeToolArgs_producesDirectlyUnmarshalableJSON(t *testing.T) {
	got, err := NormalizeToolArgs("openai", "get_symbol", 0, `{"fq_name": "com.acme.A", "depth": 2}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		FQName string `json:"fq_name"`
		Depth  int    `json:"depth"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("not unmarshalable: %v (%s)", err, got)
	}
	if out.FQName != "com.acme.A" || out.Depth != 2 {
		t.Errorf("decoded = %+v", out)
	}
	// Compacted, so equal arguments compare equal regardless of provider whitespace.
	if string(got) != `{"fq_name":"com.acme.A","depth":2}` {
		t.Errorf("not compacted: %s", got)
	}
}

func TestNormalizeToolArgs_invalidIsAttributableError(t *testing.T) {
	_, err := NormalizeToolArgs("ollama", "find_tests", 3, `{"q": `)
	if err == nil {
		t.Fatal("expected an error")
	}
	var argsErr *ToolCallArgsError
	if !errors.As(err, &argsErr) {
		t.Fatalf("want *ToolCallArgsError, got %T", err)
	}
	if argsErr.Provider != "ollama" || argsErr.Tool != "find_tests" || argsErr.Index != 3 {
		t.Errorf("error loses attribution: %+v", argsErr)
	}
	if argsErr.Unwrap() == nil {
		t.Error("underlying JSON error should be unwrappable")
	}
}

// Non-object JSON is still valid JSON. Rejecting it here would be a policy decision the contract
// should not make — a tool whose schema is an array or a scalar is unusual but legal.
func TestNormalizeToolArgs_allowsNonObjectJSON(t *testing.T) {
	for _, in := range []string{`[1,2]`, `"text"`, `42`} {
		if _, err := NormalizeToolArgs("p", "t", 0, in); err != nil {
			t.Errorf("NormalizeToolArgs(%q) rejected valid JSON: %v", in, err)
		}
	}
}

// The contract must express a turn with several calls and their matching results without any
// provider-specific shape. This is the transcript B20's loop will build.
func TestContract_expressesParallelToolCallTranscript(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "write a test"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"A"}`)},
			{ID: "b", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"B"}`)},
		}},
		{Role: RoleTool, ToolCallID: "a", Content: "class A {}"},
		{Role: RoleTool, ToolCallID: "b", Content: "class B {}"},
	}
	if len(msgs[1].ToolCalls) != 2 {
		t.Fatal("assistant message must carry multiple calls")
	}
	byID := map[string]string{}
	for _, m := range msgs {
		if m.Role == RoleTool {
			byID[m.ToolCallID] = m.Content
		}
	}
	for _, tc := range msgs[1].ToolCalls {
		if _, ok := byID[tc.ID]; !ok {
			t.Errorf("no result message answers call %q", tc.ID)
		}
	}
}
