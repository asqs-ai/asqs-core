package llmfix

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

type fixToolRegistry struct {
	mu    sync.Mutex
	calls []string
}

func (r *fixToolRegistry) Definitions() []model.ToolDefinition {
	return []model.ToolDefinition{{Name: "get_symbol", Description: "look up a symbol"}}
}

func (r *fixToolRegistry) Invoke(_ context.Context, name string, _ json.RawMessage) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
	return "public void save(Order o) {}", nil
}

func (r *fixToolRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// fixScriptedCompleter asks for a tool call on the first n completions, then answers.
type fixScriptedCompleter struct {
	mu        sync.Mutex
	toolTurns int
	seen      int
	sawTools  bool
}

func (c *fixScriptedCompleter) Complete(_ context.Context, _ []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen++
	if len(opts.Tools) > 0 {
		c.sawTools = true
	}
	if c.toolTurns > 0 {
		c.toolTurns--
		return &model.CompleteResult{ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"com.acme.OrderRepository"}`)},
		}}, nil
	}
	return &model.CompleteResult{Content: `{"src/Foo.java":"fixed"}`}, nil
}

type fixToolAuditor struct {
	mu      sync.Mutex
	steps   []string
	payload []map[string]interface{}
}

func (a *fixToolAuditor) Log(_ context.Context, step string, p interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps = append(a.steps, step)
	m, _ := p.(map[string]interface{})
	a.payload = append(a.payload, m)
}

// The acceptance the plan asks for, phrased as the failure it must detect: upstream shipped a
// tool-enabled fixer that made ZERO tool calls, and nothing caught it. This asserts a
// tools-enabled fix round actually calls, and every call is audited under the fixer's own step.
func TestCompleteToolAware_makesAndAuditsToolCalls(t *testing.T) {
	reg := &fixToolRegistry{}
	aud := &fixToolAuditor{}
	f := &Fixer{
		LLM:      &fixScriptedCompleter{toolTurns: 2},
		Audit:    aud,
		Tools:    reg,
		ToolLoop: tools.LoopOptions{Mode: tools.ModeNative},
	}

	res, err := f.completeToolAware(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "fix this"}},
		model.CompleteOptions{}, &tools.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "fixed") {
		t.Fatalf("content = %q, want the final answer", res.Content)
	}
	if reg.count() != 2 {
		t.Fatalf("the fixer made %d tool call(s), want 2 — a tools-enabled round that makes none is the exact upstream failure", reg.count())
	}
	var attempts int
	for i, s := range aud.steps {
		if s != FixToolAttemptStep {
			continue
		}
		attempts++
		if aud.payload[i]["tool"] != "get_symbol" || aud.payload[i]["ok"] != true {
			t.Errorf("attempt payload wrong: %+v", aud.payload[i])
		}
	}
	if attempts != 2 {
		t.Fatalf("audited %d attempt(s) under %s, want 2: %v", attempts, FixToolAttemptStep, aud.steps)
	}
}

// A Fixer without tools must produce byte-identical requests to the pre-tools fixer: no tool fields
// on the wire at all. That invariant is what makes the fix-quality A/B trustworthy — the control
// arm has to be the old fixer, not a new one holding an empty toolbox.
func TestCompleteToolAware_withoutToolsSendsNoToolFields(t *testing.T) {
	reg := &fixToolRegistry{}
	for _, tc := range []struct {
		name string
		f    *Fixer
	}{
		{"no registry", &Fixer{ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}},
		{"zero mode", &Fixer{Tools: reg}},
		{"resolved one-shot", &Fixer{Tools: reg, ToolLoop: tools.LoopOptions{Mode: tools.ModeOneShot}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := &fixScriptedCompleter{toolTurns: 1}
			tc.f.LLM = cc
			if _, err := tc.f.completeToolAware(context.Background(), nil, model.CompleteOptions{}, &tools.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			if cc.sawTools {
				t.Error("tool definitions reached the wire on a one-shot fixer")
			}
			if reg.count() != 0 {
				t.Errorf("one-shot fixer invoked %d tool(s)", reg.count())
			}
			if tc.f.resolvedToolMode() != tools.ModeOneShot {
				t.Errorf("resolvedToolMode = %q, want one_shot", tc.f.resolvedToolMode())
			}
		})
	}
}

// The system prompt must tell the model the tools exist; the fixer has no inventory to carry that
// signal, unlike the generator.
func TestFix_systemNoteAppearsOnlyWithTools(t *testing.T) {
	if strings.Contains(fixToolsSystemNote, "get_symbol") == false {
		t.Fatal("the tools note must name the tools")
	}
	withTools := &Fixer{Tools: &fixToolRegistry{}, ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}
	if withTools.resolvedToolMode() == tools.ModeOneShot {
		t.Fatal("a native-mode fixer with a registry must not resolve to one-shot")
	}
	plain := &Fixer{}
	if plain.resolvedToolMode() != tools.ModeOneShot {
		t.Fatal("a fixer without tools must resolve to one-shot, which is what suppresses the note")
	}
}

// tools_mode is what lets a fix-quality A/B split runs by what the model could actually DO, rather
// than by config intent.
func TestFixRequestAuditMetadata_reportsToolMode(t *testing.T) {
	plain := (&Fixer{}).FixRequestAuditMetadata()
	if plain["tools_mode"] != string(tools.ModeOneShot) {
		t.Errorf("tools_mode = %v, want one_shot", plain["tools_mode"])
	}
	withTools := (&Fixer{Tools: &fixToolRegistry{}, ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}).FixRequestAuditMetadata()
	if withTools["tools_mode"] != string(tools.ModeNative) {
		t.Errorf("tools_mode = %v, want native", withTools["tools_mode"])
	}
}
