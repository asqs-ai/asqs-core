package evaluator

import (
	"context"
	"strings"
	"testing"
)

// F10. evaluator.final.<step> rows carried no message, so every step listing that renders the
// message field showed them blank. The message must name the step, the verdict and the first
// line of the summary.
func TestAuditFinalStep_carriesMessage(t *testing.T) {
	aud := &recordingAuditor{}
	auditFinalStep(context.Background(), aud, StepResult{Step: StepTest, OK: false, Summary: "[test-failure excerpt: 113 of 662 log lines]\nTypeError: boom", DurationMs: 17513})
	auditFinalStep(context.Background(), aud, StepResult{Step: StepCompile, OK: true, Summary: "compile ok"})

	fail := aud.lastPayload("evaluator.final.test")
	if fail == nil {
		t.Fatal("failed step must be audited under evaluator.final.test")
	}
	msg, _ := fail["message"].(string)
	if !strings.Contains(msg, "test step failed") || !strings.Contains(msg, "[test-failure excerpt: 113 of 662 log lines]") || strings.Contains(msg, "TypeError") {
		t.Errorf("message must carry step, verdict and the summary's first line only; got %q", msg)
	}
	okp := aud.lastPayload("evaluator.final.compile")
	if okp == nil || okp["message"] != "compile step ok: compile ok" {
		t.Errorf("passing step message = %v", okp)
	}
	for _, k := range []string{"step", "ok", "summary", "duration_ms"} {
		if _, present := fail[k]; !present {
			t.Errorf("existing payload field %q must remain", k)
		}
	}
}

func TestFinalStepMessage_emptySummary(t *testing.T) {
	if got := finalStepMessage(StepResult{Step: StepLint, OK: true}); got != "lint step ok." {
		t.Errorf("got %q", got)
	}
}
