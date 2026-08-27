package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// The forced final turn is where run api-eb300211385b9616dc6cf81bd513369b died: the model hit the
// turn cap mid-lookup, the loop forced an answer with no textual signal that tools were gone, the
// reply came back empty twice, and the run ended fixer_response_unusable. These tests pin the
// hardening: an explicit answer-now message every provider can read, wire-level forbidding only
// where a provider declares it honours tool_choice "none" alongside tools, and one bounded retry
// for an empty reply.

// capsDecl wraps a scriptedCompleter with a capability declaration.
type capsDecl struct {
	*scriptedCompleter
	caps model.Capabilities
}

func (c capsDecl) Capabilities() model.Capabilities { return c.caps }

func exhaustTurns() []*model.CompleteResult {
	// MaxTurns 2 with the model still asking on both turns forces the final turn.
	return []*model.CompleteResult{toolCallTurn(ToolGetSymbol), toolCallTurn(ToolGetSymbol)}
}

func TestFinalTurn_appendsAnswerNowMessage(t *testing.T) {
	cc := &scriptedCompleter{turns: exhaustTurns()}
	if _, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99}); err != nil {
		t.Fatal(err)
	}
	final := cc.requests[len(cc.requests)-1]
	last := final[len(final)-1]
	if last.Role != model.RoleUser || !strings.Contains(last.Content, "No further tool calls") {
		t.Fatalf("final turn must end on the answer-now user message, got role=%q content=%q", last.Role, last.Content)
	}
	// The message is the FORCED turn's addition only — tool turns must not carry it.
	for i := 0; i < len(cc.requests)-1; i++ {
		for _, m := range cc.requests[i] {
			if strings.Contains(m.Content, "No further tool calls") {
				t.Fatalf("answer-now message leaked into tool turn %d", i)
			}
		}
	}
}

// A provider declaring ToolChoiceNoneWithTools keeps the tool declarations on the forced turn.
// On Anthropic this is a validity requirement — tool_use/tool_result history without a tools field
// is rejected — and on OpenAI it keeps the cached prefix intact.
func TestFinalTurn_keepsToolsWhenProviderHonoursNone(t *testing.T) {
	cc := capsDecl{
		scriptedCompleter: &scriptedCompleter{turns: exhaustTurns()},
		caps:              model.Capabilities{ToolCalling: true, ToolChoiceNoneWithTools: true},
	}
	if _, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99}); err != nil {
		t.Fatal(err)
	}
	final := cc.opts[len(cc.opts)-1]
	if len(final.Tools) == 0 {
		t.Error("declared ToolChoiceNoneWithTools: the forced turn must keep the tools field")
	}
	if final.ToolChoice != model.ToolChoiceNone {
		t.Errorf("forced turn ToolChoice = %q, want %q", final.ToolChoice, model.ToolChoiceNone)
	}
}

// A provider declaring it does NOT honour tool_choice none with tools (Ollama: /api/chat has no
// tool_choice, and the client rejects a forcing value when tools are declared) must get the forced
// turn WITHOUT the tools field — sending both would fail the request outright.
func TestFinalTurn_withholdsToolsWhenProviderDeclaresNoSupport(t *testing.T) {
	cc := capsDecl{
		scriptedCompleter: &scriptedCompleter{turns: exhaustTurns()},
		caps:              model.Capabilities{ToolCalling: true, ToolChoiceNoneWithTools: false},
	}
	if _, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99}); err != nil {
		t.Fatal(err)
	}
	if got := cc.opts[len(cc.opts)-1].Tools; len(got) != 0 {
		t.Errorf("forced turn carried tools on a provider that cannot combine them with tool_choice none: %v", got)
	}
}

// An empty forced-turn reply gets exactly one corrective retry; a usable retry reply is returned
// with the emptiness recorded in Warnings so the caller's audit says what happened.
func TestFinalTurn_retriesOnceOnEmptyReply(t *testing.T) {
	turns := append(exhaustTurns(),
		&model.CompleteResult{Content: ""},                   // forced final: empty
		&model.CompleteResult{Content: `{"a.java":"fixed"}`}, // corrective retry: usable
	)
	cc := &scriptedCompleter{turns: turns}
	res, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != `{"a.java":"fixed"}` {
		t.Fatalf("retry answer lost: %q", res.Content)
	}
	if cc.calls != 4 {
		t.Errorf("calls = %d, want 4 (2 turns + empty final + one retry)", cc.calls)
	}
	retryReq := cc.requests[len(cc.requests)-1]
	last := retryReq[len(retryReq)-1]
	if !strings.Contains(last.Content, "previous reply was empty") {
		t.Errorf("retry must carry the sharper instruction, got %q", last.Content)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "returned no content") {
			found = true
		}
	}
	if !found {
		t.Errorf("the empty forced turn must be reported in Warnings, got %v", res.Warnings)
	}
}

// A non-empty forced-turn reply must not pay for a retry — the guard is for emptiness only.
func TestFinalTurn_noRetryWhenReplyUsable(t *testing.T) {
	turns := append(exhaustTurns(), &model.CompleteResult{Content: "answer"})
	cc := &scriptedCompleter{turns: turns}
	res, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99})
	if err != nil {
		t.Fatal(err)
	}
	if cc.calls != 3 {
		t.Errorf("calls = %d, want 3 — a usable reply must not trigger the retry", cc.calls)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("no warnings expected on a clean forced turn, got %v", res.Warnings)
	}
}

// Prompted mode has nothing to withhold on the wire, so the appended message is its only forcing
// signal — it must be there too.
func TestFinalTurn_promptedModeGetsAnswerNowMessage(t *testing.T) {
	promptedCall := &model.CompleteResult{Content: `{"tool": "get_symbol", "arguments": {"fq_name": "A"}}`}
	cc := &scriptedCompleter{turns: []*model.CompleteResult{promptedCall, promptedCall}}
	if _, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModePrompted, MaxTurns: 2, MaxCallsPerRun: 99}); err != nil {
		t.Fatal(err)
	}
	final := cc.requests[len(cc.requests)-1]
	last := final[len(final)-1]
	if !strings.Contains(last.Content, "No further tool calls") {
		t.Fatalf("prompted-mode forced turn must carry the answer-now message, got %q", last.Content)
	}
	if len(cc.opts[len(cc.opts)-1].Tools) != 0 {
		t.Error("prompted mode must never put tool fields on the wire")
	}
}
