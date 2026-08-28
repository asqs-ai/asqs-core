package pipeline

import (
	"context"
	"fmt"
	"sort"
)

// auditGenerateFailed records that one gap produced no artifact, and why.
//
// Generation failure is the single most common way a run ends up with nothing, and until this event
// existed it was the one step that left NO trace in the audit log: the error went into
// GapOutcome.Err, which is an in-memory field the JSONL sink never sees. A run whose every gap
// failed therefore ended on a generate.prompt_budget line and simply stopped — the log said what was
// about to be asked of the model and then went silent, which reads exactly like a killed process.
//
// That is not a hypothetical. A real run against a Spring Boot repository assembled all 24 prompts
// and failed all 24 calls in three seconds against an Ollama model that was not pulled; the audit
// file recorded 172 lines, every one of them info, and none of them the words "not found".
func auditGenerateFailed(ctx context.Context, audit runAuditor, symbol, reason, detail string) {
	if audit == nil {
		return
	}
	audit.LogError(ctx, "generate.failed", map[string]interface{}{
		"message": fmt.Sprintf("Generation produced no artifact for %s: %s", symbol, detail),
		"symbol":  symbol,
		"reason":  reason,
		"error":   detail,
	})
}

// auditNoArtifacts records the terminal state of a run that generated nothing at all.
//
// This path returns a nil error and exits 0 — the run "succeeded" at producing zero tests — so
// without an event at error level the audit log's only evidence is an absence. The per-gap
// generate.failed lines above say what happened one gap at a time; this says it once, with the
// failures grouped, so the reason is legible without reading 24 lines.
func auditNoArtifacts(ctx context.Context, audit runAuditor, outcomes []GapOutcome) {
	if audit == nil {
		return
	}
	// A run with no gaps at all is a different state from a run whose gaps all failed, and saying
	// "all 0 gap(s) failed" would send a reader looking for failures that do not exist.
	if len(outcomes) == 0 {
		audit.LogError(ctx, "generate.no_artifacts", map[string]interface{}{
			"message":      "No test files were generated: the plan contained no gaps, so there was nothing to generate.",
			"gaps_total":   0,
			"distinct":     0,
			"skipped_eval": true,
		})
		return
	}

	byErr := map[string]int{}
	for _, o := range outcomes {
		if o.Generated {
			continue
		}
		e := o.Err
		if e == "" {
			e = "(no error recorded)"
		}
		byErr[e]++
	}
	// A distinct error per gap would make this payload unbounded, and the tail of a long list is
	// never the interesting part: report the most frequent errors and say how many were dropped.
	type pair struct {
		err string
		n   int
	}
	pairs := make([]pair, 0, len(byErr))
	for e, n := range byErr {
		pairs = append(pairs, pair{e, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].err < pairs[j].err
	})
	const maxReported = 5
	reported := pairs
	if len(reported) > maxReported {
		reported = reported[:maxReported]
	}
	errs := make([]map[string]interface{}, 0, len(reported))
	for _, p := range reported {
		errs = append(errs, map[string]interface{}{"error": p.err, "gaps": p.n})
	}
	payload := map[string]interface{}{
		"message": fmt.Sprintf(
			"No test files were generated: all %d gap(s) failed, so evaluation is skipped and the run ends with nothing to ship.",
			len(outcomes)),
		"gaps_total":   len(outcomes),
		"distinct":     len(pairs),
		"top_errors":   errs,
		"skipped_eval": true,
	}
	if len(pairs) > maxReported {
		payload["errors_not_listed"] = len(pairs) - maxReported
	}
	audit.LogError(ctx, "generate.no_artifacts", payload)
}
