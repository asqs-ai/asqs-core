package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// scriptedCompleter replays canned turns and records every request it received.
type scriptedCompleter struct {
	mu       sync.Mutex
	turns    []*model.CompleteResult
	calls    int
	requests [][]model.Message
	opts     []model.CompleteOptions
	// inFlight tracks concurrent Complete calls, to prove the loop does not hold a limiter slot.
	inFlight    int
	maxInFlight int
	onComplete  func()
}

func (s *scriptedCompleter) Complete(_ context.Context, msgs []model.Message, o model.CompleteOptions) (*model.CompleteResult, error) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	i := s.calls
	s.calls++
	s.requests = append(s.requests, append([]model.Message(nil), msgs...))
	s.opts = append(s.opts, o)
	s.mu.Unlock()

	if s.onComplete != nil {
		s.onComplete()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	if i < len(s.turns) {
		return s.turns[i], nil
	}
	return &model.CompleteResult{Content: "final answer"}, nil
}

type scriptedTools struct {
	defs   []model.ToolDefinition
	result string
	err    error
	calls  int
	mu     sync.Mutex
	onCall func()
}

func (s *scriptedTools) Definitions() []model.ToolDefinition { return s.defs }
func (s *scriptedTools) Invoke(context.Context, string, json.RawMessage) (string, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.onCall != nil {
		s.onCall()
	}
	if s.err != nil {
		return "", s.err
	}
	return s.result, nil
}

func loopTools(result string) *scriptedTools {
	return &scriptedTools{
		defs:   []model.ToolDefinition{{Name: ToolGetSymbol, Description: "fetch", Schema: rawJSON(`{"type":"object"}`)}},
		result: result,
	}
}

func toolCallTurn(name string) *model.CompleteResult {
	return &model.CompleteResult{
		StopReason: "tool_calls",
		ToolCalls:  []model.ToolCall{{ID: "c1", Name: name, Args: json.RawMessage(`{"fq_name":"A"}`)}},
	}
}

// Acceptance criterion: tools disabled must be byte-identical to pre-wave behaviour — one call, no
// tool fields, the caller's options untouched.
func TestCompleteWithTools_oneShotIsUnchanged(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{{Content: "answer"}}}
	reg := loopTools("body")

	res, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "write a test"}},
		model.CompleteOptions{MaxTokens: 512},
		LoopOptions{Mode: ModeOneShot})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "answer" {
		t.Errorf("content = %q", res.Content)
	}
	if cc.calls != 1 {
		t.Errorf("made %d calls; one-shot must make exactly 1", cc.calls)
	}
	if len(cc.opts[0].Tools) != 0 || cc.opts[0].ToolChoice != "" {
		t.Errorf("tool fields leaked into a one-shot request: %+v", cc.opts[0])
	}
	if len(cc.requests[0]) != 1 {
		t.Errorf("messages were modified: %+v", cc.requests[0])
	}
	if reg.calls != 0 {
		t.Errorf("tools invoked in one-shot mode")
	}
}

// The model asking for nothing must cost exactly one call.
func TestCompleteWithTools_noToolCallsReturnsImmediately(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{{Content: "done"}}}
	res, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative})
	if err != nil {
		t.Fatal(err)
	}
	if cc.calls != 1 || res.Content != "done" {
		t.Errorf("calls = %d, content = %q", cc.calls, res.Content)
	}
	if len(cc.opts[0].Tools) == 0 {
		t.Error("native mode must advertise the tools")
	}
}

func TestCompleteWithTools_runsToolAndFeedsResultBack(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol),
		{Content: "final"},
	}}
	reg := loopTools("public void quote() {}")

	res, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "final" {
		t.Errorf("content = %q", res.Content)
	}
	if reg.calls != 1 {
		t.Errorf("tool invoked %d times, want 1", reg.calls)
	}
	// The second request must contain the assistant turn and the tool result, correlated by id.
	second := cc.requests[1]
	var sawAssistant, sawResult bool
	for _, m := range second {
		if m.Role == model.RoleAssistant && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "c1" {
			sawAssistant = true
		}
		if m.Role == model.RoleTool && m.ToolCallID == "c1" && strings.Contains(m.Content, "public void quote") {
			sawResult = true
		}
	}
	if !sawAssistant || !sawResult {
		t.Errorf("transcript incomplete: assistant=%v result=%v\n%+v", sawAssistant, sawResult, second)
	}
}

// Turn budget: a model that keeps asking must still produce an answer, via one final call with
// tools withheld.
func TestCompleteWithTools_forcesAnAnswerWhenTurnsRunOut(t *testing.T) {
	// Exactly MaxTurns tool-call turns, so the forced final call falls through to a real answer.
	// Scripting more would have the forced turn return tool calls too, which is a separate case —
	// see TestCompleteWithTools_returnsFinalTurnEvenIfProviderIgnoresToolChoiceNone.
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol), toolCallTurn(ToolGetSymbol),
	}}
	reg := loopTools("body")

	res, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99})
	if err != nil {
		t.Fatal(err)
	}
	// 2 tool turns + 1 forced final turn.
	if cc.calls != 3 {
		t.Errorf("calls = %d, want 3 (2 turns + forced final)", cc.calls)
	}
	if res.Content != "final answer" {
		t.Errorf("no answer produced: %q", res.Content)
	}
	// The forced turn must withhold tools so the model cannot ask again.
	last := cc.opts[len(cc.opts)-1]
	if last.ToolChoice != model.ToolChoiceNone {
		t.Errorf("final turn tool_choice = %q, want %q", last.ToolChoice, model.ToolChoiceNone)
	}
}

func TestCompleteWithTools_capsCallsPerTurn(t *testing.T) {
	many := &model.CompleteResult{ToolCalls: []model.ToolCall{
		{ID: "a", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "b", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "c", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "d", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "e", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
	}}
	cc := &scriptedCompleter{turns: []*model.CompleteResult{many, {Content: "final"}}}
	reg := loopTools("body")

	if _, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxCallsPerTurn: 2}); err != nil {
		t.Fatal(err)
	}
	if reg.calls != 2 {
		t.Errorf("executed %d calls in one turn; cap was 2", reg.calls)
	}
}

// A pathological gap must not starve the shared limiter.
func TestCompleteWithTools_capsCallsPerRun(t *testing.T) {
	always := make([]*model.CompleteResult, 20)
	for i := range always {
		always[i] = toolCallTurn(ToolGetSymbol)
	}
	cc := &scriptedCompleter{turns: always}
	reg := loopTools("body")

	if _, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 50, MaxCallsPerTurn: 1, MaxCallsPerRun: 3}); err != nil {
		t.Fatal(err)
	}
	if reg.calls != 3 {
		t.Errorf("executed %d tool calls; per-run cap was 3", reg.calls)
	}
}

// Acceptance criterion: tool results draw from the same allowance as the prompt, so a model that
// pulls large chunks gets TRUNCATED results rather than an overflowing context.
func TestCompleteWithTools_truncatesResultsAtTheSharedBudget(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol),
		{Content: "final"},
	}}
	reg := loopTools(strings.Repeat("x", 10000))

	if _, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxResultChars: 100}); err != nil {
		t.Fatal(err)
	}
	var toolMsg string
	for _, m := range cc.requests[len(cc.requests)-1] {
		if m.Role == model.RoleTool {
			toolMsg = m.Content
		}
	}
	if len(toolMsg) > 400 {
		t.Errorf("tool result not truncated: %d chars", len(toolMsg))
	}
	if !strings.Contains(toolMsg, "truncated") {
		t.Errorf("truncation must be visible to the model: %q", toolMsg)
	}
}

// A failed lookup is information, not a turn-ending error.
func TestCompleteWithTools_toolErrorIsFedBackNotReturned(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol),
		{Content: "final"},
	}}
	reg := loopTools("")
	reg.err = errors.New("no symbol named com.acme.Nope is indexed")

	res, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative})
	if err != nil {
		t.Fatalf("a failed lookup aborted the loop: %v", err)
	}
	if res.Content != "final" {
		t.Errorf("content = %q", res.Content)
	}
	var fed bool
	for _, m := range cc.requests[1] {
		if m.Role == model.RoleTool && strings.Contains(m.Content, "is indexed") {
			fed = true
		}
	}
	if !fed {
		t.Error("the model was not told why the lookup failed")
	}
}

// Every call must be auditable, and results must NOT be recorded — B13 established that bodies do
// not belong in the audit log.
func TestCompleteWithTools_auditsEveryAttempt(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol),
		toolCallTurn(ToolGetSymbol),
		{Content: "final"},
	}}
	reg := loopTools("public void quote() {}")

	var attempts []Attempt
	if _, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 3, OnAttempt: func(a Attempt) { attempts = append(attempts, a) }}); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("recorded %d attempts, want 2", len(attempts))
	}
	if attempts[0].Turn != 0 || attempts[1].Turn != 1 {
		t.Errorf("turn indices wrong: %d, %d", attempts[0].Turn, attempts[1].Turn)
	}
	a := attempts[0]
	if a.Tool != ToolGetSymbol || a.ResultChars != len("public void quote() {}") {
		t.Errorf("attempt = %+v", a)
	}
	if !strings.Contains(string(a.Arguments), "fq_name") {
		t.Errorf("arguments not recorded: %s", a.Arguments)
	}
	// The struct must have no field carrying the result body.
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "public void quote") {
		t.Errorf("the tool result body leaked into the audit record: %s", raw)
	}
}

// Prompted mode: one call per turn, parsed out of the reply, with the catalog in the system prompt.
func TestCompleteWithTools_promptedMode(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		{Content: "I need a lookup.\n```json\n{\"tool\":\"get_symbol\",\"arguments\":{\"fq_name\":\"A\"}}\n```"},
		{Content: "final"},
	}}
	reg := loopTools("public void quote() {}")

	res, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{
			{Role: model.RoleSystem, Content: "You write tests."},
			{Role: model.RoleUser, Content: "x"},
		}, model.CompleteOptions{}, LoopOptions{Mode: ModePrompted})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "final" {
		t.Errorf("content = %q", res.Content)
	}
	if reg.calls != 1 {
		t.Errorf("tool calls = %d, want 1", reg.calls)
	}
	// Instructions must be appended to the existing system message, not added as a second one.
	sys := 0
	var sysContent string
	for _, m := range cc.requests[0] {
		if m.Role == model.RoleSystem {
			sys++
			sysContent = m.Content
		}
	}
	if sys != 1 {
		t.Errorf("system messages = %d, want 1", sys)
	}
	if !strings.Contains(sysContent, "You write tests.") || !strings.Contains(sysContent, "Available lookups") {
		t.Errorf("system message wrong:\n%s", sysContent)
	}
	// Prompted mode must not send native tool fields.
	if len(cc.opts[0].Tools) != 0 {
		t.Error("prompted mode sent native tool definitions")
	}
}

// The review focus: extra round trips must not hold a concurrency slot while a tool handler runs.
// The limiter wraps Complete, so handlers executing BETWEEN calls is what keeps 16 gaps from
// serializing behind whichever ones are looking things up.
func TestCompleteWithTools_doesNotHoldTheLimiterAcrossToolCalls(t *testing.T) {
	lim := model.NewLLMLimiter(1)
	var duringHandler int
	var mu sync.Mutex

	cc := &scriptedCompleter{turns: []*model.CompleteResult{
		toolCallTurn(ToolGetSymbol), {Content: "final"},
	}}
	limited := model.NewConcurrencyLimitedCompleter(cc, lim)

	reg := loopTools("body")
	reg.onCall = func() {
		// If the loop held the single slot across the handler, this acquire would block forever.
		done := make(chan struct{})
		go func() {
			other := model.NewConcurrencyLimitedCompleter(&scriptedCompleter{}, lim)
			_, _ = other.Complete(context.Background(), nil, model.CompleteOptions{})
			mu.Lock()
			duringHandler++
			mu.Unlock()
			close(done)
		}()
		select {
		case <-done:
		case <-context.Background().Done():
		}
	}

	if _, err := CompleteWithTools(context.Background(), limited, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if duringHandler == 0 {
		t.Error("no other LLM call could proceed while a tool handler ran; the loop is holding a " +
			"limiter slot across handler execution and will serialize concurrent gaps")
	}
}

// Throughput at gap concurrency: many loops in flight must all finish.
func TestCompleteWithTools_concurrentGaps(t *testing.T) {
	lim := model.NewLLMLimiter(8)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cc := &scriptedCompleter{turns: []*model.CompleteResult{
				toolCallTurn(ToolGetSymbol), {Content: fmt.Sprintf("final %d", n)},
			}}
			limited := model.NewConcurrencyLimitedCompleter(cc, lim)
			res, err := CompleteWithTools(context.Background(), limited, loopTools("body"),
				[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
				LoopOptions{Mode: ModeNative})
			if err != nil {
				errs <- err
				return
			}
			if res.Content != fmt.Sprintf("final %d", n) {
				errs <- fmt.Errorf("gap %d got %q", n, res.Content)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCompleteWithTools_requiresACompleter(t *testing.T) {
	if _, err := CompleteWithTools(context.Background(), nil, loopTools("b"), nil,
		model.CompleteOptions{}, LoopOptions{Mode: ModeNative}); err == nil {
		t.Fatal("expected an error with no completer")
	}
}

// A provider that ignores tool_choice "none" can answer the forced turn with another tool call. The
// loop must still return — the budget is spent — rather than looping or erroring. The caller gets
// whatever the model said, which may be empty; that is a provider-compliance problem, and the loop's
// job is to terminate rather than to paper over it.
func TestCompleteWithTools_returnsFinalTurnEvenIfProviderIgnoresToolChoiceNone(t *testing.T) {
	always := make([]*model.CompleteResult, 10)
	for i := range always {
		always[i] = toolCallTurn(ToolGetSymbol)
	}
	cc := &scriptedCompleter{turns: always}

	res, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxCallsPerRun: 99})
	if err != nil {
		t.Fatalf("loop did not terminate cleanly: %v", err)
	}
	if res == nil {
		t.Fatal("no result returned")
	}
	// 2 tool turns + the forced final turn (empty, tool calls dangling) + exactly ONE corrective
	// retry. The retry is bounded: a model that stays empty must not spin the loop further.
	if cc.calls != 4 {
		t.Errorf("calls = %d, want 4 (2 turns + forced final + one bounded retry)", cc.calls)
	}
	if cc.opts[len(cc.opts)-1].ToolChoice != model.ToolChoiceNone {
		t.Error("the forced turn must still withhold tools")
	}
	// The unusable forced turn must be visible to the caller's warning audit, not silent — run
	// api-eb300211385b9616dc6cf81bd513369b burned two fixer rounds on exactly this with nothing in
	// the audit saying why.
	var sawEmpty, sawRetryEmpty bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "dangling tool call") {
			sawEmpty = true
		}
		if strings.Contains(w, "retry also returned no content") {
			sawRetryEmpty = true
		}
	}
	if !sawEmpty || !sawRetryEmpty {
		t.Errorf("warnings must name the empty forced turn and the empty retry, got %v", res.Warnings)
	}
}

// The per-run cap must bound a GAP, not a single CompleteWithTools call.
//
// Measured on a real run before this was wired: one gap made 8 loop invocations and 60 tool calls
// against a cap of 12, because the generator wraps completion in a retry loop and each call started
// a fresh budget. That is exactly the pathological gap the cap exists to stop from starving the
// shared limiter.
func TestCompleteWithTools_runBudgetSpansMultipleCalls(t *testing.T) {
	budget := &RunBudget{}
	total := 0
	for invocation := 0; invocation < 5; invocation++ {
		always := make([]*model.CompleteResult, 20)
		for i := range always {
			always[i] = toolCallTurn(ToolGetSymbol)
		}
		cc := &scriptedCompleter{turns: always}
		reg := loopTools("body")
		if _, err := CompleteWithTools(context.Background(), cc, reg,
			[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
			LoopOptions{Mode: ModeNative, MaxTurns: 50, MaxCallsPerTurn: 1, MaxCallsPerRun: 3, Budget: budget}); err != nil {
			t.Fatal(err)
		}
		total += reg.calls
	}
	if total != 3 {
		t.Errorf("%d tool calls across 5 loop invocations; the per-run cap of 3 must span them all", total)
	}
}

// Without a shared budget each call gets its own — correct only for a caller making exactly one.
func TestCompleteWithTools_nilBudgetIsPerCall(t *testing.T) {
	cc := &scriptedCompleter{turns: []*model.CompleteResult{toolCallTurn(ToolGetSymbol), {Content: "f"}}}
	reg := loopTools("body")
	if _, err := CompleteWithTools(context.Background(), cc, reg,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative}); err != nil {
		t.Fatal(err)
	}
	if reg.calls != 1 {
		t.Errorf("calls = %d", reg.calls)
	}
}

// Caps must be enforced AND audited. Enforcing silently makes a model that asked for two lookups
// indistinguishable from one that asked for ten and was cut to three, so there is no way to tell
// whether a cap is well-chosen or quietly starving the loop.
func TestCompleteWithTools_reportsEveryCapHit(t *testing.T) {
	many := &model.CompleteResult{ToolCalls: []model.ToolCall{
		{ID: "a", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "b", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "c", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
		{ID: "d", Name: ToolGetSymbol, Args: json.RawMessage(`{}`)},
	}}
	cc := &scriptedCompleter{turns: []*model.CompleteResult{many, {Content: "final"}}}

	var hits []CapHit
	if _, err := CompleteWithTools(context.Background(), cc, loopTools("body"),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxCallsPerTurn: 2,
			OnCapHit: func(h CapHit) { hits = append(hits, h) }}); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("a per-turn cap truncated the call list with no cap-hit reported")
	}
	h := hits[0]
	if h.Cap != CapCallsPerTurn || h.Requested != 4 || h.Allowed != 2 {
		t.Errorf("cap hit = %+v; want calls_per_turn requested=4 allowed=2", h)
	}
}

func TestCompleteWithTools_reportsTurnAndResultCaps(t *testing.T) {
	always := make([]*model.CompleteResult, 10)
	for i := range always {
		always[i] = toolCallTurn(ToolGetSymbol)
	}
	var hits []CapHit
	if _, err := CompleteWithTools(context.Background(), &scriptedCompleter{turns: always},
		loopTools(strings.Repeat("x", 10000)),
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxTurns: 2, MaxResultChars: 100, MaxCallsPerRun: 99,
			OnCapHit: func(h CapHit) { hits = append(hits, h) }}); err != nil {
		t.Fatal(err)
	}
	kinds := map[CapKind]bool{}
	for _, h := range hits {
		kinds[h.Cap] = true
	}
	if !kinds[CapResultChars] {
		t.Errorf("result-char truncation not reported: %+v", hits)
	}
}

// A result cut by the TOOL's own cap must set Truncated too. Observed on a real run: a 6047-char
// result (the 6000 cap plus its marker) was logged as untruncated, so a drilldown would show a
// complete body where half was missing.
func TestCompleteWithTools_reportsToolSideTruncation(t *testing.T) {
	reg := &Registry{Meta: nil, MaxChars: 50}
	// A registry with a tiny cap; the fake below routes through capped() the way handlers do.
	_ = reg

	cc := &scriptedCompleter{turns: []*model.CompleteResult{toolCallTurn(ToolGetSymbol), {Content: "f"}}}
	tr := &truncatingTools{defs: loopTools("").defs, body: strings.Repeat("y", 5000), max: 100}

	var attempts []Attempt
	if _, err := CompleteWithTools(context.Background(), cc, tr,
		[]model.Message{{Role: model.RoleUser, Content: "x"}}, model.CompleteOptions{},
		LoopOptions{Mode: ModeNative, MaxResultChars: 100000,
			OnAttempt: func(a Attempt) { attempts = append(attempts, a) }}); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	if !attempts[0].Truncated {
		t.Error("a tool-side truncation was recorded as untruncated; a drilldown would show a " +
			"complete result where most of the body is missing")
	}
}

// truncatingTools mimics a Registry that cuts at its own MaxChars and reports it.
type truncatingTools struct {
	defs []model.ToolDefinition
	body string
	max  int
	cut  bool
}

func (t *truncatingTools) Definitions() []model.ToolDefinition { return t.defs }
func (t *truncatingTools) Invoke(context.Context, string, json.RawMessage) (string, error) {
	out, cut := truncateN(t.body, t.max)
	t.cut = cut
	return out, nil
}
func (t *truncatingTools) LastResultTruncated() bool { return t.cut }
