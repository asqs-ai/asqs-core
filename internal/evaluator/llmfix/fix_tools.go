package llmfix

import (
	"context"
	"fmt"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

// Audit steps for the fixer's tool loop. Deliberately parallel to the generator's
// generate.tool_call / generate.tool_cap, with the same payload shape, so one reader renders both
// without learning a second shape.
const (
	FixToolAttemptStep = "fix.tool_call"
	FixToolCapStep     = "fix.tool_cap"
)

// fixToolsSystemNote is appended to the system prompt when the fixer has tool access. The
// generator's equivalent lives in the retrieval context (the inventory names what is fetchable);
// the fixer has no inventory — its context is an error log and an artifact — so the steer to look
// things up has to be stated in the prompt.
const fixToolsSystemNote = `

You have READ-ONLY lookup tools against the repository index: get_symbol (exact signature and
location for a fully-qualified name), expand_symbol (callers/callees), search_code, find_tests_for,
read_file_range. When a compile error involves a symbol whose signature, visibility or overloads you
have not been shown — "cannot find symbol", "is inaccessible", "no suitable method", wrong-arity
calls — LOOK IT UP before writing the fix. A guessed signature that compiles into the wrong overload
wastes an entire fix attempt. Do not modify anything you look up; tools are reference only.`

// resolvedToolMode reports the tool tier this Fixer will actually deliver.
func (f *Fixer) resolvedToolMode() tools.Mode {
	if f.Tools == nil || f.ToolLoop.Mode == "" {
		return tools.ModeOneShot
	}
	return f.ToolLoop.Mode
}

// completeToolAware performs one completion, with tool access when it is configured.
//
// A nil registry or an unset/one-shot mode is exactly one Complete with the options given — no tool
// fields on the wire — so a Fixer without tools produces byte-identical requests to before this
// bundle. That invariant is what makes the fix-quality A/B trustworthy: the control arm is the old
// fixer, not a new fixer with an empty toolbox.
func (f *Fixer) completeToolAware(ctx context.Context, messages []model.Message, opts model.CompleteOptions, budget *tools.RunBudget) (*model.CompleteResult, error) {
	if f.Tools == nil || f.ToolLoop.Mode == "" || f.ToolLoop.Mode == tools.ModeOneShot {
		return f.LLM.Complete(ctx, messages, opts)
	}
	loop := f.ToolLoop
	// Hooks are attached here rather than stored on the Fixer so they carry this call's ctx, which
	// is what ties an attempt to its run in the audit log. (Upstream additionally composes in a
	// context-carried observer installed by the session runner; the session engine is outside
	// core's seam, so there is nothing to compose with here.)
	if loop.OnAttempt == nil {
		loop.OnAttempt = f.auditFixToolAttempts(ctx)
	}
	if loop.OnCapHit == nil {
		loop.OnCapHit = f.auditFixToolCapHit(ctx)
	}
	// The budget is supplied by Fix, not created here: this function runs once per retry attempt,
	// so a budget created here would reset on every retry and the per-run cap would bound nothing —
	// the same defect the generator's loop had before its budget was hoisted.
	loop.Budget = budget
	return tools.CompleteWithTools(ctx, f.LLM, f.Tools, messages, opts, loop)
}

// auditFixToolAttempts returns an OnAttempt hook recording every lookup, or nil when there is no
// auditor — nil rather than a no-op closure keeps the loop's hot path free.
//
// Unlike the generator's hook this logs failures through the same Log call with ok=false: FixAudit
// is deliberately a one-method interface so llmfix does not depend on the pipeline's Auditor, and
// widening it for a severity split would break every implementor for no query the payload's ok
// field does not already answer.
func (f *Fixer) auditFixToolAttempts(ctx context.Context) func(tools.Attempt) {
	if f.Audit == nil {
		return nil
	}
	return func(a tools.Attempt) {
		msg := fmt.Sprintf("Fix tool %s (turn %d) returned %d chars in %dms.", a.Tool, a.Turn, a.ResultChars, a.DurationMS)
		if a.Err != "" {
			msg = fmt.Sprintf("Fix tool %s (turn %d) failed: %s", a.Tool, a.Turn, a.Err)
		}
		payload := map[string]interface{}{
			"message":        msg,
			"tool":           a.Tool,
			"turn":           a.Turn,
			"idx":            a.Index,
			"input_summary":  map[string]interface{}{"tool": a.Tool, "arguments": a.Arguments},
			"output_summary": map[string]interface{}{"result_chars": a.ResultChars, "truncated": a.Truncated},
			"duration_ms":    a.DurationMS,
			"ok":             a.Err == "",
		}
		if a.Err != "" {
			payload["error"] = a.Err
		}
		f.Audit.Log(ctx, FixToolAttemptStep, payload)
	}
}

// auditFixToolCapHit records every bound that took effect, or nil when there is no auditor.
func (f *Fixer) auditFixToolCapHit(ctx context.Context) func(tools.CapHit) {
	if f.Audit == nil {
		return nil
	}
	return func(h tools.CapHit) {
		f.Audit.Log(ctx, FixToolCapStep, map[string]interface{}{
			"message":   fmt.Sprintf("Fix tool cap %s: requested %d, allowed %d (turn %d).", h.Cap, h.Requested, h.Allowed, h.Turn),
			"cap":       string(h.Cap),
			"requested": h.Requested,
			"allowed":   h.Allowed,
			"turn":      h.Turn,
		})
	}
}
