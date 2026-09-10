package pipeline

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

type sandboxWiringAuditor struct{}

func (sandboxWiringAuditor) Log(context.Context, string, interface{})      {}
func (sandboxWiringAuditor) LogError(context.Context, string, interface{}) {}

// runner.eval_plan_resolved is only worth having if the eval sandbox is actually given the
// auditor. The field is optional — nothing downstream fails when it is unset, the event simply
// never appears, which is the state this was meant to fix.
func TestNewEvalSandbox_carriesTheAuditor(t *testing.T) {
	sb := newEvalSandbox(&config.Config{}, sandboxWiringAuditor{})
	if sb == nil {
		t.Fatal("nil sandbox")
	}
	if sb.Audit == nil {
		t.Fatal("the eval sandbox got no auditor, so runner.eval_plan_resolved can never be emitted")
	}
}

// A nil auditor must stay nil rather than becoming a non-nil interface wrapping a nil value, which
// would defeat auditEvalPlan's own nil guard and panic on first use.
func TestNewEvalSandbox_nilAuditorStaysNil(t *testing.T) {
	sb := newEvalSandbox(&config.Config{}, nil)
	if sb == nil {
		t.Fatal("nil sandbox")
	}
	if sb.Audit != nil {
		t.Fatalf("Audit = %#v, want nil", sb.Audit)
	}
}
