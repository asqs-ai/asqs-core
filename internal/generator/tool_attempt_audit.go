package generator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

// ToolAttemptStep is the audit step name for one model-issued index lookup during generation.
const ToolAttemptStep = "generate.tool_call"

// AttemptSummary is the payload shape for a tool-call attempt.
//
// The nested input_summary / output_summary shape is upstream's and is kept field for field: it
// mirrors a persisted-attempt wire contract there, and diverging would cost a second shape for no
// benefit here.
//
// The result BODY is deliberately absent. The audit sink established that prompt and response
// bodies do not belong in the audit log; a tool result is a chunk body, which is exactly that.
// ResultChars gives a reader the size it needs without storing the content.
type AttemptSummary struct {
	Tool        string          `json:"tool"`
	Turn        int             `json:"turn"`
	Idx         int             `json:"idx"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	ResultChars int             `json:"result_chars"`
	Truncated   bool            `json:"truncated,omitempty"`
	DurationMs  int64           `json:"duration_ms"`
	OK          bool            `json:"ok"`
	Error       string          `json:"error,omitempty"`
}

// auditToolAttempts returns an OnAttempt hook that records every lookup, or nil when there is no
// auditor.
//
// Returning nil rather than a no-op closure is what keeps the loop's hot path free: the loop skips
// the call entirely when the hook is nil, so an unaudited run pays nothing.
func (g *LLMGenerator) auditToolAttempts(ctx context.Context) func(tools.Attempt) {
	if g.Audit == nil {
		return nil
	}
	return func(a tools.Attempt) {
		s := AttemptSummary{
			Tool: a.Tool, Turn: a.Turn, Idx: a.Index,
			Arguments: a.Arguments, ResultChars: a.ResultChars,
			Truncated: a.Truncated, DurationMs: a.DurationMS,
			OK: a.Err == "", Error: a.Err,
		}
		msg := fmt.Sprintf("Tool %s (turn %d) returned %d chars in %dms.", a.Tool, a.Turn, a.ResultChars, a.DurationMS)
		if a.Err != "" {
			msg = fmt.Sprintf("Tool %s (turn %d) failed: %s", a.Tool, a.Turn, a.Err)
		}
		payload := map[string]interface{}{
			"message": msg,
			"tool":    s.Tool,
			"turn":    s.Turn,
			"idx":     s.Idx,
			// input/output summaries are nested, per the shape noted on AttemptSummary.
			"input_summary":  map[string]interface{}{"tool": s.Tool, "arguments": s.Arguments},
			"output_summary": map[string]interface{}{"result_chars": s.ResultChars, "truncated": s.Truncated},
			"duration_ms":    s.DurationMs,
			"ok":             s.OK,
		}
		if s.Error != "" {
			payload["error"] = s.Error
			g.Audit.LogError(ctx, ToolAttemptStep, payload)
			return
		}
		g.Audit.Log(ctx, ToolAttemptStep, payload)
	}
}

// ToolCapStep is the audit step for a bound that truncated or ended the tool loop.
const ToolCapStep = "generate.tool_cap"

// auditToolCapHit records every cap that took effect.
//
// Caps must be enforced *and* audited; upstream shipped only the enforcement half at first. Without this,
// a model that asked for two lookups and a model that asked for ten and was cut to three are
// indistinguishable in the log — so there is no way to tell whether a cap is well-chosen or is
// quietly starving the loop.
func (g *LLMGenerator) auditToolCapHit(ctx context.Context) func(tools.CapHit) {
	if g.Audit == nil {
		return nil
	}
	return func(h tools.CapHit) {
		g.Audit.Log(ctx, ToolCapStep, map[string]interface{}{
			"message": fmt.Sprintf("Tool cap %s hit on turn %d: requested %d, allowed %d.",
				h.Cap, h.Turn, h.Requested, h.Allowed),
			"cap":       string(h.Cap),
			"requested": h.Requested,
			"allowed":   h.Allowed,
			"turn":      h.Turn,
		})
	}
}
