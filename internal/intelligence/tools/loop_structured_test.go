package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// capsCompleter is a scripted completer that declares capabilities.
type capsCompleter struct {
	caps    model.Capabilities
	calls   []model.CompleteOptions
	replies []*model.CompleteResult
}

func (c *capsCompleter) Complete(ctx context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	c.calls = append(c.calls, opts)
	i := len(c.calls) - 1
	if i < len(c.replies) {
		return c.replies[i], nil
	}
	return &model.CompleteResult{Content: "{}"}, nil
}

func (c *capsCompleter) Name() string                     { return "caps" }
func (c *capsCompleter) Capabilities() model.Capabilities { return c.caps }

// undeclaredCompleter has no capability declaration at all.
type undeclaredCompleter struct{ calls []model.CompleteOptions }

func (c *undeclaredCompleter) Complete(ctx context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	c.calls = append(c.calls, opts)
	return &model.CompleteResult{Content: "{}"}, nil
}
func (c *undeclaredCompleter) Name() string { return "undeclared" }

func structuredProbeSchema() *model.StructuredJSONSchema {
	return &model.StructuredJSONSchema{Name: "probe", Schema: json.RawMessage(`{"type":"object"}`)}
}

func probeRegistry() ToolInvoker {
	return scriptedRegistry{defs: []model.ToolDefinition{{
		Name: "get_symbol", Description: "probe", Schema: json.RawMessage(`{"type":"object"}`),
	}}}
}

type scriptedRegistry struct{ defs []model.ToolDefinition }

func (r scriptedRegistry) Definitions() []model.ToolDefinition { return r.defs }
func (r scriptedRegistry) Invoke(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return "signature: public interface Pageable", nil
}

// On a provider whose structured output is a grammar over the whole generation, Structured and
// Tools are mutually exclusive per request: the schema grammar excludes the model's tool-call
// syntax, so a request carrying both LOOKS tool-enabled and never calls. Measured live: the fixer
// made 0 tool calls across 4 native-mode attempts on Ollama, while an identical prompt without
// `format` called get_symbol on every trial. The loop must therefore withhold Structured from
// tool-offering turns and re-apply it on the final, tool-free turn.
func TestCompleteWithTools_defersStructuredWhenProviderCannotCombine(t *testing.T) {
	cc := &capsCompleter{
		caps: model.Capabilities{StructuredOutput: true, ToolCalling: true, StructuredWithTools: false},
		replies: []*model.CompleteResult{
			// Turn 0: model calls the tool — which is only possible because format was withheld.
			{ToolCalls: []model.ToolCall{{ID: "1", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"Pageable"}`)}}},
			// Turn 1: keeps asking; turn budget (2) forces a final turn after this.
			{ToolCalls: []model.ToolCall{{ID: "2", Name: "get_symbol", Args: json.RawMessage(`{"fq_name":"Page"}`)}}},
			// Final turn: schema-constrained answer.
			{Content: `{"a.java":"fixed"}`},
		},
	}
	opts := model.CompleteOptions{Structured: structuredProbeSchema(), MaxTokens: 100}
	res, err := CompleteWithTools(context.Background(), cc, probeRegistry(), []model.Message{{Role: "user", Content: "q"}},
		opts, LoopOptions{Mode: ModeNative, MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != `{"a.java":"fixed"}` {
		t.Fatalf("unexpected final content: %q", res.Content)
	}
	if len(cc.calls) != 3 {
		t.Fatalf("got %d provider calls, want 3 (2 tool turns + final)", len(cc.calls))
	}
	for i := 0; i < 2; i++ {
		if cc.calls[i].Structured != nil {
			t.Errorf("turn %d carried Structured alongside Tools; on this provider that silently disables tool calling", i)
		}
		if len(cc.calls[i].Tools) == 0 {
			t.Errorf("turn %d carried no tools", i)
		}
	}
	final := cc.calls[2]
	if final.Structured == nil {
		t.Error("the final turn lost Structured; the ANSWER must still be schema-constrained")
	}
	if len(final.Tools) != 0 {
		t.Error("the final turn still carried tools next to Structured")
	}
	if final.ToolChoice != model.ToolChoiceNone {
		t.Errorf("final turn ToolChoice = %q, want %q", final.ToolChoice, model.ToolChoiceNone)
	}
	// The original options must not have been mutated: the caller may reuse them.
	if opts.Structured == nil {
		t.Error("CompleteWithTools mutated the caller's options")
	}
}

// A provider that composes the two keeps Structured on every turn — deferring there would give up
// schema constraint for nothing.
func TestCompleteWithTools_keepsStructuredWhenProviderCombines(t *testing.T) {
	cc := &capsCompleter{
		caps: model.Capabilities{StructuredOutput: true, ToolCalling: true, StructuredWithTools: true},
		replies: []*model.CompleteResult{
			{ToolCalls: []model.ToolCall{{ID: "1", Name: "get_symbol", Args: json.RawMessage(`{}`)}}},
			{Content: `{"a.java":"fixed"}`},
		},
	}
	_, err := CompleteWithTools(context.Background(), cc, probeRegistry(), []model.Message{{Role: "user", Content: "q"}},
		model.CompleteOptions{Structured: structuredProbeSchema()}, LoopOptions{Mode: ModeNative, MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if cc.calls[0].Structured == nil {
		t.Error("Structured was deferred although the provider declares it composes with tools")
	}
}

// An undeclared completer is unknown, not incapable: today's behaviour is kept.
func TestCompleteWithTools_undeclaredCompleterKeepsStructured(t *testing.T) {
	cc := &undeclaredCompleter{}
	_, err := CompleteWithTools(context.Background(), cc, probeRegistry(), []model.Message{{Role: "user", Content: "q"}},
		model.CompleteOptions{Structured: structuredProbeSchema()}, LoopOptions{Mode: ModeNative, MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.calls) == 0 || cc.calls[0].Structured == nil {
		t.Error("Structured was stripped for an undeclared completer; unknown is not incapable")
	}
}
