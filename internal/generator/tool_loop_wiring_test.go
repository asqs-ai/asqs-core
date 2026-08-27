package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

// countingRegistry is a ToolInvoker that records every call and returns a fixed body.
type countingRegistry struct {
	mu    sync.Mutex
	calls []string
	body  string
}

func (r *countingRegistry) Definitions() []model.ToolDefinition {
	return []model.ToolDefinition{{Name: "get_symbol", Description: "look up a symbol"}}
}

func (r *countingRegistry) Invoke(_ context.Context, name string, _ json.RawMessage) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
	if r.body == "" {
		return "class A {}", nil
	}
	return r.body, nil
}

func (r *countingRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// scriptedCompleter returns a tool call for the first n completions, then plain text. failAt, when
// set, fails that completion (1-based) so an attempt can spend budget before it dies.
type scriptedCompleter struct {
	mu        sync.Mutex
	toolTurns int
	seen      int
	failAt    int
	failErr   error
}

func (c *scriptedCompleter) Complete(_ context.Context, _ []model.Message, _ model.CompleteOptions) (*model.CompleteResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen++
	if c.failAt > 0 && c.seen == c.failAt {
		return nil, c.failErr
	}
	if c.toolTurns > 0 {
		c.toolTurns--
		return &model.CompleteResult{ToolCalls: []model.ToolCall{
			{ID: fmt.Sprintf("c%d", c.seen), Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"com.acme.A"}`)},
		}}, nil
	}
	return &model.CompleteResult{Content: "done"}, nil
}

// The wiring is the deliverable: a registry and a resolved loop mode on the generator must make
// generation actually call tools AND record every attempt. Enforcement and auditing are separately
// tested in the loop and audit-hook tests; this pins that the generator connects the two, which is
// the seam where a tool-enabled run can silently behave one-shot.
func TestCompleteOnce_invokesToolsAndAuditsAttempts(t *testing.T) {
	reg := &countingRegistry{}
	aud := &toolAttemptAuditor{}
	g := &LLMGenerator{
		LLM:      &scriptedCompleter{toolTurns: 2},
		Tools:    reg,
		ToolLoop: tools.LoopOptions{Mode: tools.ModeNative},
		Audit:    aud,
	}

	res, err := g.completeOnce(context.Background(), []model.Message{{Role: model.RoleUser, Content: "write a test"}},
		model.CompleteOptions{}, &tools.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Content) != "done" {
		t.Fatalf("content = %q, want the final answer", res.Content)
	}
	if reg.count() != 2 {
		t.Fatalf("tool invoked %d time(s), want 2 — the registry is not reaching the loop", reg.count())
	}
	if len(aud.steps) != 2 {
		t.Fatalf("audited %d attempt(s): %v — every lookup must be recorded", len(aud.steps), aud.steps)
	}
	for _, s := range aud.steps {
		if s != ToolAttemptStep {
			t.Errorf("step = %q, want %q", s, ToolAttemptStep)
		}
	}
	p, _ := aud.payloads[0].(map[string]interface{})
	in, _ := p["input_summary"].(map[string]interface{})
	if p["tool"] != "get_symbol" || in["arguments"] == nil {
		t.Errorf("attempt must record the tool name and its arguments: %+v", p)
	}
}

// A generator with no registry (or a one-shot mode) must not touch the loop at all: that is the
// byte-identical pre-tools path, and it is what every run with tools disabled takes.
func TestCompleteOnce_withoutToolsIsTheOneShotPath(t *testing.T) {
	reg := &countingRegistry{}
	for _, tc := range []struct {
		name string
		g    *LLMGenerator
	}{
		{"no registry", &LLMGenerator{LLM: &scriptedCompleter{toolTurns: 1}, ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}},
		{"zero mode", &LLMGenerator{LLM: &scriptedCompleter{toolTurns: 1}, Tools: reg}},
		{"one-shot mode", &LLMGenerator{LLM: &scriptedCompleter{toolTurns: 1}, Tools: reg, ToolLoop: tools.LoopOptions{Mode: tools.ModeOneShot}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.g.completeOnce(context.Background(), nil, model.CompleteOptions{}, &tools.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			if reg.count() != 0 {
				t.Fatalf("one-shot path invoked %d tool(s)", reg.count())
			}
		})
	}
}

// The per-run cap bounds a GAP, not a completion. completeGenerateWithRetry may call completeOnce
// several times for one gap (a transient failure, a truncation escalation), and a budget created
// per attempt would reset each time — upstream measured one gap making 8 loop invocations and 60
// tool calls against a cap of 12 before the budget was hoisted.
func TestCompleteGenerateWithRetry_sharesOneBudgetAcrossAttempts(t *testing.T) {
	reg := &countingRegistry{}
	// The first attempt spends two lookups and THEN dies on a transient failure, so the retry
	// starts with budget already consumed. Shared: 2 + 1 = the cap of 3. Per-attempt: the retry
	// would get a fresh 3 and the model asks for more turns than that.
	cc := &scriptedCompleter{toolTurns: 8, failAt: 3, failErr: fmt.Errorf("connection reset by peer")}
	g := &LLMGenerator{
		LLM:      cc,
		Tools:    reg,
		ToolLoop: tools.LoopOptions{Mode: tools.ModeNative, MaxCallsPerRun: 3},
	}

	if _, err := g.completeGenerateWithRetry(context.Background(),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := reg.count(); got > 3 {
		t.Fatalf("%d tool calls for one gap against a per-run cap of 3 — the budget reset between attempts", got)
	}
}
