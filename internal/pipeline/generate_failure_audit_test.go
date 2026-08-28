package pipeline

import (
	"context"
	"strings"
	"testing"
)

// levelledAuditor records step names, payloads and the level each was logged at, which is what
// separates these events from the info-level narration around them.
type levelledAuditor struct {
	steps    []string
	levels   []string
	payloads []map[string]interface{}
}

func (a *levelledAuditor) record(level, step string, payload interface{}) {
	a.steps = append(a.steps, step)
	a.levels = append(a.levels, level)
	m, _ := payload.(map[string]interface{})
	a.payloads = append(a.payloads, m)
}

func (a *levelledAuditor) Log(_ context.Context, step string, payload interface{}) {
	a.record("info", step, payload)
}

func (a *levelledAuditor) LogError(_ context.Context, step string, payload interface{}) {
	a.record("error", step, payload)
}

func (a *levelledAuditor) find(step string) (map[string]interface{}, string, bool) {
	for i, s := range a.steps {
		if s == step {
			return a.payloads[i], a.levels[i], true
		}
	}
	return nil, "", false
}

// GUARD: a gap that generates nothing must say so in the audit log.
//
// The error used to live only in GapOutcome.Err — an in-memory field the JSONL sink never sees — so
// a run whose every gap failed ended on a generate.prompt_budget line and stopped, indistinguishable
// in the log from a killed process.
func TestAuditGenerateFailed_recordsSymbolReasonAndError(t *testing.T) {
	a := &levelledAuditor{}
	auditGenerateFailed(context.Background(), a, "com.example.Owner#getPet", "generator_error",
		"ollama: model 'qwen2.5-coder:32b' not found")

	payload, level, ok := a.find("generate.failed")
	if !ok {
		t.Fatalf("generate.failed was not logged: %v", a.steps)
	}
	if level != "error" {
		t.Errorf("generate.failed logged at %q; a gap that produced nothing is not info", level)
	}
	if got := payload["symbol"]; got != "com.example.Owner#getPet" {
		t.Errorf("symbol = %v", got)
	}
	if got := payload["reason"]; got != "generator_error" {
		t.Errorf("reason = %v", got)
	}
	// The provider's own words are the whole value of the event: "not found" is what turns an
	// eight-minute investigation into one grep.
	if got, _ := payload["error"].(string); !strings.Contains(got, "not found") {
		t.Errorf("error = %q; the underlying message must survive verbatim", got)
	}
}

// GUARD: a run that generates nothing at all returns nil and exits 0, so the audit log is the only
// place the zero-artifact outcome can be recorded.
func TestAuditNoArtifacts_groupsFailuresAndCaps(t *testing.T) {
	outcomes := []GapOutcome{
		{Symbol: "a.B#c", Err: "model not found"},
		{Symbol: "a.B#d", Err: "model not found"},
		{Symbol: "a.B#e", Err: "empty generation"},
		{Symbol: "a.B#f"}, // no error recorded at all
	}
	a := &levelledAuditor{}
	auditNoArtifacts(context.Background(), a, outcomes)

	payload, level, ok := a.find("generate.no_artifacts")
	if !ok {
		t.Fatalf("generate.no_artifacts was not logged: %v", a.steps)
	}
	if level != "error" {
		t.Errorf("generate.no_artifacts logged at %q; a run with nothing to ship is not info", level)
	}
	if got := payload["gaps_total"]; got != 4 {
		t.Errorf("gaps_total = %v, want 4", got)
	}
	if got := payload["distinct"]; got != 3 {
		t.Errorf("distinct = %v, want 3 (two errors plus the unrecorded one)", got)
	}
	top, _ := payload["top_errors"].([]map[string]interface{})
	if len(top) != 3 {
		t.Fatalf("top_errors has %d entries, want 3: %v", len(top), top)
	}
	// Most frequent first, so the dominant failure is the first thing read.
	if top[0]["error"] != "model not found" || top[0]["gaps"] != 2 {
		t.Errorf("top_errors[0] = %v; want the 2-gap error first", top[0])
	}
	if _, listed := payload["errors_not_listed"]; listed {
		t.Error("errors_not_listed must be absent when everything fit")
	}

	// A distinct error per gap would make the payload unbounded; the overflow is counted, not dumped.
	many := make([]GapOutcome, 0, 9)
	for i := 0; i < 9; i++ {
		many = append(many, GapOutcome{Symbol: "s", Err: string(rune('a'+i)) + " failed"})
	}
	b := &levelledAuditor{}
	auditNoArtifacts(context.Background(), b, many)
	p2, _, _ := b.find("generate.no_artifacts")
	if got, _ := p2["top_errors"].([]map[string]interface{}); len(got) != 5 {
		t.Errorf("top_errors = %d entries, want the 5-entry cap", len(got))
	}
	if got := p2["errors_not_listed"]; got != 4 {
		t.Errorf("errors_not_listed = %v, want 4", got)
	}
}

// GUARD: a generated gap is not a failure. Counting it would make the summary lie in the one
// direction that matters — reporting failures on a run that actually shipped tests.
func TestAuditNoArtifacts_ignoresGeneratedOutcomes(t *testing.T) {
	a := &levelledAuditor{}
	auditNoArtifacts(context.Background(), a, []GapOutcome{
		{Symbol: "ok", Generated: true},
		{Symbol: "bad", Err: "boom"},
	})
	payload, _, ok := a.find("generate.no_artifacts")
	if !ok {
		t.Fatal("event not logged")
	}
	if got := payload["distinct"]; got != 1 {
		t.Errorf("distinct = %v, want 1 — a generated outcome is not a failure", got)
	}
}

// GUARD: an empty plan is not a run full of failures. "all 0 gap(s) failed" would send a reader
// hunting for errors that were never emitted.
func TestAuditNoArtifacts_emptyPlanSaysSo(t *testing.T) {
	a := &levelledAuditor{}
	auditNoArtifacts(context.Background(), a, nil)
	payload, level, ok := a.find("generate.no_artifacts")
	if !ok {
		t.Fatal("event not logged for an empty plan")
	}
	if level != "error" {
		t.Errorf("level = %q", level)
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "no gaps") {
		t.Errorf("message = %q; an empty plan must say so", msg)
	}
	if strings.Contains(msg, "failed") {
		t.Errorf("message = %q; nothing failed here", msg)
	}
	if _, has := payload["top_errors"]; has {
		t.Error("top_errors must be absent when there were no gaps")
	}
}
