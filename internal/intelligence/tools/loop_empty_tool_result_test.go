package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// The bug this file pins: a RoleTool message whose Content is "" is dropped by go-openai's
// `json:"content,omitempty"`, the API reads the absent key as null, and the whole request dies
// with `Invalid value for 'content': expected a string, got null`. Run
// api-47cdc4dce89eebc4cf55208c8c3b714f lost an e2e gap to exactly that.

func toolMessages(msgs []model.Message) []model.Message {
	var out []model.Message
	for _, m := range msgs {
		if m.Role == model.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

// The shared result-char budget is spent before the call runs, so the result is dropped entirely.
// The message that answers the tool call must still carry text.
func TestCompleteWithTools_budgetExhaustedToolResultIsNeverEmpty(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{toolCallTurn(ToolGetSymbol)}}
	// A budget already at its ceiling: room <= 0 on the very first result.
	budget := &RunBudget{}
	budget.spend(0, 100)

	if _, err := CompleteWithTools(context.Background(), cc, loopTools("a result the model never sees"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxResultChars: 100, Budget: budget}); err != nil {
		t.Fatal(err)
	}

	// The forced final turn is the request that used to 400.
	final := cc.requests[len(cc.requests)-1]
	tools := toolMessages(final)
	if len(tools) == 0 {
		t.Fatal("no tool message in the final request; the tool_call would be unanswered")
	}
	for _, m := range tools {
		if strings.TrimSpace(m.Content) == "" {
			t.Fatalf("tool message %q has empty content — this is the 400", m.ToolCallID)
		}
		if !strings.Contains(m.Content, "budget") {
			t.Errorf("dropped result should say why it is missing, got %q", m.Content)
		}
	}
}

// A tool that legitimately finds nothing returns ("", nil). That reaches the same wire hazard by a
// different road, so it needs the same guarantee.
func TestCompleteWithTools_emptyToolResultIsNeverEmptyOnTheWire(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{toolCallTurn(ToolGetSymbol)}}

	if _, err := CompleteWithTools(context.Background(), cc, loopTools(""),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxResultChars: 100000}); err != nil {
		t.Fatal(err)
	}

	for _, req := range cc.requests {
		for _, m := range toolMessages(req) {
			if strings.TrimSpace(m.Content) == "" {
				t.Fatalf("empty tool result reached the wire as empty content (id %q)", m.ToolCallID)
			}
		}
	}
}

func TestToolResultContent(t *testing.T) {
	if got := toolResultContent("body", false); got != "body" {
		t.Errorf("non-empty content must pass through, got %q", got)
	}
	if got := toolResultContent("", true); got != toolResultDroppedNote {
		t.Errorf("dropped result note = %q", got)
	}
	if got := toolResultContent("   ", false); got != toolResultEmptyNote {
		t.Errorf("whitespace-only content is empty for this purpose, got %q", got)
	}
}

// The cap record used to compute Requested AFTER blanking `out`, so it always reported 0 and the
// audit could never show how much was actually dropped.
func TestCompleteWithTools_droppedResultCapReportsRealSize(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{toolCallTurn(ToolGetSymbol)}}
	budget := &RunBudget{}
	budget.spend(0, 100)

	result := strings.Repeat("x", 512)
	var hits []CapHit
	if _, err := CompleteWithTools(context.Background(), cc, loopTools(result),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{
			Mode: ModeNative, MaxResultChars: 100, Budget: budget,
			OnCapHit: func(h CapHit) { hits = append(hits, h) },
		}); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, h := range hits {
		if h.Cap != CapResultChars || h.Allowed != 0 {
			continue
		}
		found = true
		if h.Requested != len(result) {
			t.Errorf("Requested = %d, want the real result size %d", h.Requested, len(result))
		}
	}
	if !found {
		t.Fatalf("no dropped-result cap hit recorded; got %+v", hits)
	}
}
