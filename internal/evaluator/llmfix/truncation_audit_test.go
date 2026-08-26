package llmfix

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// payloadAudit keeps the payloads, not just the step names: the point of these events is the
// numbers they carry.
type payloadAudit struct {
	steps    []string
	payloads []map[string]interface{}
}

func (a *payloadAudit) Log(_ context.Context, step string, payload interface{}) {
	a.steps = append(a.steps, step)
	if m, ok := payload.(map[string]interface{}); ok {
		a.payloads = append(a.payloads, m)
		return
	}
	a.payloads = append(a.payloads, nil)
}

func (a *payloadAudit) find(step string) map[string]interface{} {
	for i, s := range a.steps {
		if s == step {
			return a.payloads[i]
		}
	}
	return nil
}

func (a *payloadAudit) findLast(step string) map[string]interface{} {
	var out map[string]interface{}
	for i, s := range a.steps {
		if s == step {
			out = a.payloads[i]
		}
	}
	return out
}

func truncatedErr(maxTokens, got int) error {
	return &model.TruncatedCompletionError{
		Provider: "ollama", Reason: "length",
		MaxTokens: maxTokens, GotTokens: got,
		Content: `{"tests/FooTests.cs":"public class FooTests { void A(){`,
	}
}

// bumpForTruncation used to fire silently, so a round that hit the output cap, doubled its budget
// and retried was indistinguishable in the log from one that never hit it. On a local deployment
// that distinction is the whole diagnosis.
func TestFix_auditsAnOutputCapTruncationAndTheRetry(t *testing.T) {
	llm := &recordingCompleter{
		errs:    []error{truncatedErr(8192, 8192)},
		replies: []string{"", `{"tests/FooTests.cs":"public class FooTests { void A(){} }"}`},
	}
	aud := &payloadAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	if _, err := f.Fix(context.Background(), fixReqOneArtifact()); err != nil {
		t.Fatalf("the retry at a larger cap should have succeeded: %v", err)
	}

	p := aud.find("fix.completion_truncated")
	if p == nil {
		t.Fatalf("truncation was not audited; steps = %v", aud.steps)
	}
	if p["retrying"] != true {
		t.Errorf("retrying = %v, want true", p["retrying"])
	}
	if p["max_tokens_was"] != 8192 || p["max_tokens_now"] != 16384 {
		t.Errorf("the bump must be recorded, got was=%v now=%v", p["max_tokens_was"], p["max_tokens_now"])
	}
	if p["stop_reason"] != "length" || p["provider"] != "ollama" {
		t.Errorf("the provider's own verdict must be carried, got %v / %v", p["provider"], p["stop_reason"])
	}
	// Retrying at a larger cap is only visible if the request actually changed.
	if len(llm.opts) < 2 || llm.opts[1].MaxTokens != 16384 {
		t.Errorf("the retry did not use the raised cap: %+v", llm.opts)
	}
}

// The case that leaves no trace at all today: the cap was hit while already at the ceiling, so the
// round dies and the audit shows only a parse classification read off the text.
func TestFix_auditsATruncationWithNoHeadroomLeft(t *testing.T) {
	// The first truncation bumps 8192 -> 16384; the second is already at the ceiling, which is the
	// case with nowhere left to go.
	llm := &recordingCompleter{errs: []error{
		truncatedErr(8192, 8192), truncatedErr(16384, 16384),
		truncatedErr(16384, 16384), truncatedErr(16384, 16384),
		truncatedErr(16384, 16384),
	}}
	aud := &payloadAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	if _, err := f.Fix(context.Background(), fixReqOneArtifact()); err == nil {
		t.Fatal("a round that cannot finish its reply must fail")
	}
	p := aud.findLast("fix.completion_truncated")
	if p == nil {
		t.Fatalf("a terminal truncation must be audited; steps = %v", aud.steps)
	}
	if p["retrying"] != false {
		t.Errorf("retrying = %v, want false at the ceiling", p["retrying"])
	}
	if msg, _ := p["message"].(string); !strings.Contains(msg, "no headroom left") {
		t.Errorf("the message must say why no retry followed, got %q", msg)
	}
}

type usageCompleter struct{ reply string }

func (c *usageCompleter) Complete(_ context.Context, _ []model.Message, _ model.CompleteOptions) (*model.CompleteResult, error) {
	return &model.CompleteResult{
		Content:    c.reply,
		StopReason: "stop",
		Usage:      &model.Usage{PromptTokens: 12000, CompletionTokens: 640, TotalTokens: 12640},
	}, nil
}

// bumpForTruncation reports was=0 when it declines, which is not a cap anyone set. Logging that as
// the prior limit reads as "the previous cap was zero".
func TestFix_noHeadroomAuditReportsTheCapThatWasInForce(t *testing.T) {
	llm := &recordingCompleter{errs: []error{
		truncatedErr(8192, 8192), truncatedErr(16384, 16384),
		truncatedErr(16384, 16384), truncatedErr(16384, 16384),
		truncatedErr(16384, 16384),
	}}
	aud := &payloadAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	if _, err := f.Fix(context.Background(), fixReqOneArtifact()); err == nil {
		t.Fatal("a round that cannot finish its reply must fail")
	}
	p := aud.findLast("fix.completion_truncated")
	if p["max_tokens_was"] != maxFixerOutputTokens || p["max_tokens_now"] != maxFixerOutputTokens {
		t.Errorf("was=%v now=%v, want both at the ceiling of %d", p["max_tokens_was"], p["max_tokens_now"], maxFixerOutputTokens)
	}
}

// (Upstream additionally tests auditCompletionShape and the single-file plain-source fallback
// here — both arrive with the fixer robustness batches in CP53; port those tests with them.)
