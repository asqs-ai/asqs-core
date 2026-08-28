package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// Loop bounds. Small on purpose: the pipeline runs up to gap_concurrency (16) gaps against a shared
// LLM limiter of 8, so every extra turn is a round trip multiplied across gaps. An unbounded loop is
// a cost and latency hazard, not merely a slow one.
const (
	DefaultMaxToolTurns        = 4
	DefaultMaxToolCallsPerTurn = 3
	DefaultMaxToolCallsPerRun  = 12
	// DefaultMaxToolResultChars is the total characters tool results may add across the whole loop.
	// Results draw from the same allowance as the prompt: a model that pulls ten large chunks gets
	// truncated results rather than an overflowing context window.
	DefaultMaxToolResultChars = 24000
)

// Attempt is one tool call, recorded for audit and for the session drilldown.
//
// Arguments are kept but results are not: a result is a chunk body, and B13 established that prompt
// and response bodies do not belong in the audit log. ResultChars is what a drilldown needs to show
// size without storing the content.
type Attempt struct {
	Turn        int
	Index       int
	Tool        string
	Arguments   json.RawMessage
	ResultChars int
	Truncated   bool
	Err         string
	DurationMS  int64
}

// RunBudget carries tool spend across every CompleteWithTools call made for one gap.
//
// The per-run cap is worthless without it. The generator wraps completion in a retry loop and may
// call it several times per gap; a budget scoped to a single call resets each time. Measured on a
// real run: one gap made EIGHT loop invocations and 60 tool calls against a cap of 12 — precisely
// the pathological gap the cap exists to stop from starving the shared limiter.
//
// Safe for concurrent gaps because each gap gets its own; it is never shared across gaps.
type RunBudget struct {
	mu          sync.Mutex
	calls       int
	resultChars int
}

// spend records n calls and c result characters, returning the running totals.
func (b *RunBudget) spend(n, c int) (calls, chars int) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls += n
	b.resultChars += c
	return b.calls, b.resultChars
}

// totals reports current spend without changing it.
func (b *RunBudget) totals() (calls, chars int) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.resultChars
}

// CapKind names which bound stopped the loop, for the audit record.
type CapKind string

const (
	CapCallsPerTurn CapKind = "calls_per_turn"
	CapCallsPerRun  CapKind = "calls_per_run"
	CapTurns        CapKind = "turns"
	CapResultChars  CapKind = "result_chars"
)

// CapHit describes a bound that took effect. Enforcing a cap silently makes it impossible to tell a
// model that asked for little from one that was cut off — B20 requires caps to be enforced AND
// audited, and only the enforcement half shipped initially.
type CapHit struct {
	Cap       CapKind
	Requested int
	Allowed   int
	Turn      int
}

// LoopOptions bounds one CompleteWithTools call.
type LoopOptions struct {
	Mode            Mode
	MaxTurns        int
	MaxCallsPerTurn int
	MaxCallsPerRun  int
	MaxResultChars  int
	OnAttempt       func(Attempt)
	// OnCapHit is called whenever a bound truncates or ends the loop. Optional.
	OnCapHit func(CapHit)
	// OnNoDefinitions is called when a tool-enabled loop finds the registry advertises nothing, so
	// every turn will be tool-free. Optional; without it that degradation is silent, and silence
	// makes "the model did not call tools" indistinguishable from "no tools were ever offered".
	OnNoDefinitions func()
	// Budget carries spend across every call made for one gap. Nil gives this call its own budget,
	// which is only correct when the caller makes exactly one call per gap.
	Budget *RunBudget
}

func (o LoopOptions) turns() int {
	if o.MaxTurns > 0 {
		return o.MaxTurns
	}
	return DefaultMaxToolTurns
}
func (o LoopOptions) callsPerTurn() int {
	if o.MaxCallsPerTurn > 0 {
		return o.MaxCallsPerTurn
	}
	return DefaultMaxToolCallsPerTurn
}
func (o LoopOptions) callsPerRun() int {
	if o.MaxCallsPerRun > 0 {
		return o.MaxCallsPerRun
	}
	return DefaultMaxToolCallsPerRun
}
func (o LoopOptions) resultChars() int {
	if o.MaxResultChars > 0 {
		return o.MaxResultChars
	}
	return DefaultMaxToolResultChars
}

// CompleteWithTools runs the model→tool→model loop and returns the final completion.
//
// # One-shot is byte-identical
//
// ModeOneShot (or a nil registry) performs exactly one Complete with the options it was given and
// no tool fields, so a run with tools disabled produces the same request bytes as before this wave.
//
// # The limiter is never held across a tool handler
//
// The concurrency limiter wraps Complete and releases when Complete returns, so handlers execute
// between calls rather than inside one. That matters: the pipeline runs 16 gaps against a limiter
// of 8, and holding a slot while reading the index would serialize the whole run behind whichever
// gaps happened to be looking things up.
//
// # Termination
//
// The loop ends when the model stops asking for tools, when the turn budget is spent, or when a
// call cap is hit. On budget exhaustion it makes ONE more call with tools withheld, so the caller
// always gets an answer rather than a dangling tool request.
func CompleteWithTools(
	ctx context.Context,
	cc model.ChatCompleter,
	reg ToolInvoker,
	messages []model.Message,
	opts model.CompleteOptions,
	loop LoopOptions,
) (*model.CompleteResult, error) {
	if cc == nil {
		return nil, fmt.Errorf("tools: a chat completer is required")
	}
	defs := definitionsFor(reg)
	// A registry that advertises NOTHING degrades to a plain completion. That is the right
	// behaviour and the wrong silence: the caller resolved a tool mode, audited it, and believes
	// this run has tool access — while every turn is tool-free and no event says so. It is the
	// same invisible-no-op shape as an inert config key, and it is exactly what makes "the model
	// did not call tools" indistinguishable from "the model was never offered any".
	//
	// OnNoDefinitions lets the caller record the difference. Nil keeps the old behaviour.
	if loop.Mode != ModeOneShot && len(defs) == 0 && loop.OnNoDefinitions != nil {
		loop.OnNoDefinitions()
	}
	if loop.Mode == ModeOneShot || len(defs) == 0 {
		return cc.Complete(ctx, messages, opts)
	}

	convo := append([]model.Message(nil), messages...)
	if loop.Mode == ModePrompted {
		convo = withPromptedInstructions(convo, defs)
	}

	// Structured output and tool calling do not compose on every provider. Where structured output
	// is a grammar constraint over the whole generation (Ollama's `format`), the schema excludes
	// the model's tool-call syntax — native calls cannot be emitted, and a prompted call's
	// `{"tool": …, "arguments": {…}}` object violates a files-map schema just as hard. The result
	// is the worst kind of failure: a request that looks tool-enabled and silently never calls.
	// Measured on qwen3-coder:30b — every trial called get_symbol without `format`, none with it,
	// while the fixer's live run made 0 calls across 4 tool-enabled attempts for exactly this
	// reason.
	//
	// So on such providers Structured is withheld from tool-offering turns and re-applied on the
	// final turn, where tools are withheld anyway and the two cannot conflict. The cost is real
	// and accepted: a model that answers WITHOUT calling tools on an early turn returns an
	// unconstrained reply, which both callers already parse defensively (that was the only
	// behaviour before structured output existed). An undeclared completer keeps both fields —
	// unknown is not incapable.
	structuredDeferred := false
	if opts.Structured != nil {
		if caps, declared := model.DeclaredCapabilitiesOf(cc); declared && !caps.StructuredWithTools {
			structuredDeferred = true
		}
	}

	budget := loop.Budget
	if budget == nil {
		// No shared budget: this call gets its own. Correct only when the caller makes exactly one
		// call per gap, which is why the generator supplies one.
		budget = &RunBudget{}
	}
	reportCap := func(h CapHit) {
		if loop.OnCapHit != nil {
			loop.OnCapHit(h)
		}
	}
	budgetSpent := false

	for turn := 0; turn < loop.turns(); turn++ {
		turnOpts := opts
		if structuredDeferred {
			turnOpts.Structured = nil
		}
		if loop.Mode == ModeNative {
			turnOpts.Tools = defs
		}
		res, err := cc.Complete(ctx, convo, turnOpts)
		if err != nil {
			return nil, err
		}

		calls := callsFromResult(loop.Mode, res, defs)
		if len(calls) == 0 {
			return res, nil
		}
		if len(calls) > loop.callsPerTurn() {
			reportCap(CapHit{Cap: CapCallsPerTurn, Requested: len(calls), Allowed: loop.callsPerTurn(), Turn: turn})
			calls = calls[:loop.callsPerTurn()]
		}
		spentCalls, spentChars := budget.totals()
		if remaining := loop.callsPerRun() - spentCalls; remaining <= 0 {
			reportCap(CapHit{Cap: CapCallsPerRun, Requested: len(calls), Allowed: 0, Turn: turn})
			return finalTurn(ctx, cc, convo, opts, loop.Mode, defs)
		} else if len(calls) > remaining {
			reportCap(CapHit{Cap: CapCallsPerRun, Requested: len(calls), Allowed: remaining, Turn: turn})
			calls = calls[:remaining]
		}
		_ = spentChars

		convo = append(convo, assistantTurn(loop.Mode, res, calls))

		for i, call := range calls {
			started := time.Now()
			out, invErr := reg.Invoke(ctx, call.Name, call.Args)

			// Results draw from the same allowance as the prompt. Once it is spent, results are
			// truncated and no further tools are offered — an overflowing window silently drops the
			// output contract, which sits last.
			// A result may be cut twice: by the tool's own MaxChars, and by the shared budget here.
			// Both must set the flag, or a drilldown shows a complete result where half is missing.
			truncated := false
			if tr, ok := reg.(interface{ LastResultTruncated() bool }); ok && tr.LastResultTruncated() {
				truncated = true
			}
			_, usedChars := budget.totals()
			dropped := false
			if room := loop.resultChars() - usedChars; room <= 0 {
				// Report before blanking: `Requested: len(out)` measured after the assignment
				// below always read 0, so the audit could never show how much was actually cut.
				reportCap(CapHit{Cap: CapResultChars, Requested: len(out), Allowed: 0, Turn: turn})
				out, truncated, budgetSpent, dropped = "", true, true, true
			} else if len(out) > room {
				reportCap(CapHit{Cap: CapResultChars, Requested: len(out), Allowed: room, Turn: turn})
				out = out[:room] + "\n… [tool result truncated: shared context budget exhausted]"
				truncated, budgetSpent = true, true
			}
			totalCalls, _ := budget.spend(1, len(out))
			_ = totalCalls

			text := out
			if invErr != nil {
				// A failed lookup is information the model can act on; an error would abort the turn
				// and tell it nothing.
				text = fmt.Sprintf("%s failed: %v", call.Name, invErr)
			}
			text = toolResultContent(text, dropped)
			convo = append(convo, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: text})

			if loop.OnAttempt != nil {
				a := Attempt{
					Turn: turn, Index: i, Tool: call.Name, Arguments: call.Args,
					ResultChars: len(out), Truncated: truncated,
					DurationMS: time.Since(started).Milliseconds(),
				}
				if invErr != nil {
					a.Err = invErr.Error()
				}
				loop.OnAttempt(a)
			}
		}

		if used, _ := budget.totals(); budgetSpent || used >= loop.callsPerRun() {
			return finalTurn(ctx, cc, convo, opts, loop.Mode, defs)
		}
	}
	// Turn budget spent with the model still asking: force an answer.
	reportCap(CapHit{Cap: CapTurns, Requested: loop.turns() + 1, Allowed: loop.turns()})
	return finalTurn(ctx, cc, convo, opts, loop.Mode, defs)
}

// finalAnswerNowMessage is appended as the final turn's user message. It is the only forcing
// signal every provider can receive: tool_choice does not exist on Ollama's /api/chat, and before
// this message existed the model saw a conversation that simply ended on a tool result — nothing
// marked the mode switch from "keep looking things up" to "answer". Run
// api-eb300211385b9616dc6cf81bd513369b hit the turn cap mid-lookup on two consecutive fixer rounds
// and returned empty content on both forced turns.
const finalAnswerNowMessage = "No further tool calls are available. Using the tool results already provided, give your complete final answer now, in the exact output format the task requires."

// finalAnswerRetryMessage replaces finalAnswerNowMessage for the single retry after an empty
// forced-turn reply.
const finalAnswerRetryMessage = "Your previous reply was empty. Produce the complete final answer NOW, in the exact output format the task requires. Do not request tools and do not return an empty reply."

// finalTurn asks for an answer with further tool calls forbidden.
//
// How the forbidding reaches the wire is capability-gated. A provider declaring
// ToolChoiceNoneWithTools keeps the tool declarations in the request with tool_choice "none" —
// on Anthropic that is a validity requirement (tool_use/tool_result history without a tools field
// is rejected), and on OpenAI it also keeps the cached prefix intact. Otherwise the tools field is
// withheld exactly as before, which is a silent no-op signal on those wires — so the appended
// finalAnswerNowMessage carries the instruction in text, the one channel every provider has.
//
// An empty reply here is retried once with a sharper instruction rather than returned as-is: the
// caller's parsers treat empty content as an unusable round, and two of those in a row end a run
// (session.run_fix_loop_fixer_unusable). Both the emptiness and the retry are reported through
// CompleteResult.Warnings, which the fixer and generator already audit.
func finalTurn(ctx context.Context, cc model.ChatCompleter, convo []model.Message, opts model.CompleteOptions, mode Mode, defs []model.ToolDefinition) (*model.CompleteResult, error) {
	final := opts
	if mode == ModeNative {
		final.ToolChoice = model.ToolChoiceNone
		if caps, declared := model.DeclaredCapabilitiesOf(cc); declared && caps.ToolChoiceNoneWithTools {
			final.Tools = defs
		}
	}
	msgs := append(append([]model.Message(nil), convo...), model.Message{Role: model.RoleUser, Content: finalAnswerNowMessage})
	res, err := cc.Complete(ctx, msgs, final)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(res.Content) != "" {
		return res, nil
	}
	warnings := append([]string(nil), res.Warnings...)
	empty := "tool_loop: the forced final turn returned no content"
	if n := len(res.ToolCalls); n > 0 {
		empty = fmt.Sprintf("tool_loop: the forced final turn returned no content and %d dangling tool call(s), which cannot be executed", n)
	}
	warnings = append(warnings, empty+"; retried once with an explicit answer-now instruction")
	retryMsgs := append(append([]model.Message(nil), convo...), model.Message{Role: model.RoleUser, Content: finalAnswerRetryMessage})
	res2, err2 := cc.Complete(ctx, retryMsgs, final)
	if err2 != nil {
		// The empty result is still a result: returning it (with the warnings saying why) lets the
		// caller's own repair path run, where propagating a retry-transport error would end the
		// round outright.
		res.Warnings = append(warnings, fmt.Sprintf("tool_loop: the retry failed: %v", err2))
		return res, nil
	}
	if strings.TrimSpace(res2.Content) == "" {
		warnings = append(warnings, "tool_loop: the retry also returned no content")
	}
	res2.Warnings = append(warnings, res2.Warnings...)
	return res2, nil
}

func definitionsFor(reg ToolInvoker) []model.ToolDefinition {
	if reg == nil {
		return nil
	}
	return reg.Definitions()
}

// callsFromResult extracts the calls a turn requested, per mode.
func callsFromResult(mode Mode, res *model.CompleteResult, defs []model.ToolDefinition) []model.ToolCall {
	if res == nil {
		return nil
	}
	if mode == ModePrompted {
		if call, ok := ParsePromptedCall(res.Content, defs); ok {
			return []model.ToolCall{*call}
		}
		return nil
	}
	return res.ToolCalls
}

// Notes substituted for a tool result that would otherwise be sent as an empty string.
const (
	toolResultDroppedNote = "[tool result omitted: the shared context budget was exhausted before this call ran; do not call this tool again this turn]"
	toolResultEmptyNote   = "[tool returned an empty result]"
)

// toolResultContent guarantees a RoleTool message carries a non-empty string.
//
// An empty one is not merely uninformative, it is fatal. OpenAI types `content` as a string on
// tool messages, and go-openai marshals the field with `json:"content,omitempty"` — so "" DROPS
// the key entirely, the API reads the absent field as null, and the whole request dies with
// `Invalid value for 'content': expected a string, got null`. That is a 400 on the very next
// Complete, which aborts the gap's generation outright. Run api-47cdc4dce89eebc4cf55208c8c3b714f
// lost its last e2e gap that way: the result-chars budget hit zero on turn 3, the result was
// blanked, and the forced final turn 400'd. Withholding the message instead is not an option
// either — every tool_call in the assistant turn must be answered by a tool message with a
// matching id, so an unanswered call is its own 400.
//
// Two callers reach here: the budget-exhausted path (dropped), and any tool that legitimately
// returns nothing for a query. Both need a note; only the first needs to tell the model to stop
// asking. The note's own characters are not charged to the budget — it is a fixed ~120 bytes on a
// budget that is already at or below zero, and charging it would only deepen the overdraft.
func toolResultContent(text string, dropped bool) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	if dropped {
		return toolResultDroppedNote
	}
	return toolResultEmptyNote
}

// assistantTurn records what the model said, in the shape the provider expects back.
//
// Prompted mode replays the raw reply as assistant text: the provider has no tool-call structure to
// receive, and stripping the model's own words would leave the transcript incoherent.
func assistantTurn(mode Mode, res *model.CompleteResult, calls []model.ToolCall) model.Message {
	if mode == ModePrompted {
		return model.Message{Role: model.RoleAssistant, Content: res.Content}
	}
	return model.Message{Role: model.RoleAssistant, Content: res.Content, ToolCalls: calls}
}

// withPromptedInstructions appends the tool catalog to the system message, or prepends one.
func withPromptedInstructions(convo []model.Message, defs []model.ToolDefinition) []model.Message {
	instr := PromptedInstructions(defs)
	if strings.TrimSpace(instr) == "" {
		return convo
	}
	for i := range convo {
		if convo[i].Role == model.RoleSystem {
			convo[i].Content = strings.TrimRight(convo[i].Content, "\n") + "\n\n" + instr
			return convo
		}
	}
	return append([]model.Message{{Role: model.RoleSystem, Content: instr}}, convo...)
}
