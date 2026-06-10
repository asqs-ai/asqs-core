package testbootstrap

import (
	"context"
)

func logAudit(audit Auditor, ctx context.Context, step string, payload map[string]interface{}) {
	if audit == nil {
		return
	}
	audit.Log(ctx, step, payload)
}

func logAuditError(audit Auditor, ctx context.Context, step string, payload map[string]interface{}) {
	if audit == nil {
		return
	}
	audit.LogError(ctx, step, payload)
}
