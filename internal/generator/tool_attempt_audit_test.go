package generator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

type toolAttemptAuditor struct {
	steps    []string
	payloads []interface{}
	errSteps []string
}

func (r *toolAttemptAuditor) Log(_ context.Context, step string, p interface{}) {
	r.steps = append(r.steps, step)
	r.payloads = append(r.payloads, p)
}
func (r *toolAttemptAuditor) LogError(_ context.Context, step string, p interface{}) {
	r.errSteps = append(r.errSteps, step)
	r.payloads = append(r.payloads, p)
}

func TestAuditToolAttempts_recordsEveryLookup(t *testing.T) {
	aud := &toolAttemptAuditor{}
	g := &LLMGenerator{Audit: aud}
	hook := g.auditToolAttempts(context.Background())
	if hook == nil {
		t.Fatal("no hook returned despite an auditor")
	}

	hook(tools.Attempt{
		Turn: 1, Index: 0, Tool: "get_symbol",
		Arguments:   json.RawMessage(`{"fq_name":"com.acme.A#b"}`),
		ResultChars: 240, DurationMS: 12,
	})
	if len(aud.steps) != 1 || aud.steps[0] != ToolAttemptStep {
		t.Fatalf("steps = %v", aud.steps)
	}
	p, _ := aud.payloads[0].(map[string]interface{})
	if p["tool"] != "get_symbol" || p["turn"] != 1 || p["ok"] != true {
		t.Errorf("payload = %+v", p)
	}
	in, _ := p["input_summary"].(map[string]interface{})
	if in == nil || in["tool"] != "get_symbol" {
		t.Errorf("input_summary missing or wrong: %+v", p["input_summary"])
	}
	out, _ := p["output_summary"].(map[string]interface{})
	if out == nil || out["result_chars"] != 240 {
		t.Errorf("output_summary missing or wrong: %+v", p["output_summary"])
	}
}

// Prompt and response bodies do not belong in the audit log (the sink redacts them everywhere
// else). A tool result is a chunk body, so only its SIZE may be recorded.
func TestAuditToolAttempts_neverRecordsTheResultBody(t *testing.T) {
	aud := &toolAttemptAuditor{}
	g := &LLMGenerator{Audit: aud}
	hook := g.auditToolAttempts(context.Background())

	const secretBody = "public void quote() { chargeTheCustomer(); }"
	hook(tools.Attempt{Tool: "get_symbol", ResultChars: len(secretBody)})

	raw, err := json.Marshal(aud.payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "chargeTheCustomer") {
		t.Fatalf("the tool result body reached the audit log: %s", raw)
	}
	if !strings.Contains(string(raw), "result_chars") {
		t.Errorf("the result SIZE must still be recorded: %s", raw)
	}
}

// A failed lookup goes to LogError so it surfaces distinctly, and must still say why.
func TestAuditToolAttempts_failuresUseLogError(t *testing.T) {
	aud := &toolAttemptAuditor{}
	g := &LLMGenerator{Audit: aud}
	hook := g.auditToolAttempts(context.Background())

	hook(tools.Attempt{Tool: "get_symbol", Err: "no symbol named com.acme.Nope is indexed"})
	if len(aud.errSteps) != 1 {
		t.Fatalf("failure not logged as an error: %v / %v", aud.steps, aud.errSteps)
	}
	p, _ := aud.payloads[0].(map[string]interface{})
	if p["ok"] != false {
		t.Errorf("ok should be false: %+v", p)
	}
	if !strings.Contains(p["error"].(string), "is indexed") {
		t.Errorf("the reason must be recorded: %+v", p["error"])
	}
	if !strings.Contains(p["message"].(string), "failed") {
		t.Errorf("message should read as a failure: %+v", p["message"])
	}
}

// No auditor means no hook, so the loop skips the call entirely rather than paying for a no-op.
func TestAuditToolAttempts_nilAuditorReturnsNilHook(t *testing.T) {
	g := &LLMGenerator{}
	if hook := g.auditToolAttempts(context.Background()); hook != nil {
		t.Error("a generator with no auditor should return a nil hook")
	}
}

// The nested payload shape is upstream's persisted-attempt contract, kept field for field so the
// two trees' audit rows stay readable by the same tooling. This pins the keys.
func TestAuditToolAttempts_payloadKeepsTheNestedSummaryShape(t *testing.T) {
	aud := &toolAttemptAuditor{}
	g := &LLMGenerator{Audit: aud}
	g.auditToolAttempts(context.Background())(tools.Attempt{
		Tool: "search_code", Turn: 2, Index: 1, ResultChars: 10, DurationMS: 5,
		Arguments: json.RawMessage(`{"query":"x"}`), Truncated: true,
	})
	p, _ := aud.payloads[0].(map[string]interface{})
	for _, k := range []string{"tool", "turn", "idx", "input_summary", "output_summary", "duration_ms", "ok"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload missing %q, which the attempt contract requires", k)
		}
	}
	out, _ := p["output_summary"].(map[string]interface{})
	if out["truncated"] != true {
		t.Errorf("truncation must be visible to a reader: %+v", out)
	}
}
