package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// evalFirstWaveMetricsForDB maps the evaluation result to metadata for
// index_runs.first_wave_metrics. Nil when evaluation errored — a zero row and an absent row must
// stay distinguishable, so a run whose evaluation never produced a verdict writes NULL rather
// than a row of zeroes that reads as "compiled nothing, fixed nothing".
//
// This is the CLI pipeline's edition of upstream's orchestrator projection: same fields, same
// snake_case JSON keys (the struct tags are the wire format — stored as JSONB verbatim). Stability
// here is what the pipeline reports to the operator (green outright, or green after discarding
// repeatedly-failing files), so the metric and the console never disagree about the same run.
func evalFirstWaveMetricsForDB(eval *evaluator.EvalWorkflowResult, evalErr error, usage *model.UsageAccumulator) *metadata.FirstWaveRunMetrics {
	if eval == nil || evalErr != nil {
		return nil
	}
	// Destructure the accumulator here, in one place: total with the belt-and-braces fallback
	// (per-call Add already sums prompt+completion when a provider omits total_tokens), and the
	// prompt share on its own — previously computed and thrown away.
	var promptTokens, llmTotalTokens int64
	if usage != nil {
		p, c, tot := usage.Totals()
		if tot <= 0 {
			tot = p + c
		}
		promptTokens, llmTotalTokens = p, tot
	}
	stable := eval.Stable || eval.EarlyExitStableAfterDiscard
	m := &metadata.FirstWaveRunMetrics{
		CompileOKAfterGenerate: eval.CompileOKAfterGenerate,
		TestOKWithoutFix:       eval.TestOKWithoutFix,
		EvalStable:             stable,
		EvalIterations:         eval.Iterations,
		CompileFixCount:        eval.CompileFixCount,
		TestFixCount:           eval.TestFixCount,
		LlmTotalTokens:         llmTotalTokens,
	}
	if promptTokens > 0 {
		m.PromptTokens = &promptTokens
	}
	if stable && llmTotalTokens > 0 {
		ts := llmTotalTokens
		m.TokensToStable = &ts
	}
	return m
}

// completeRun records the run's terminal state: status/stable/iterations via SetRunCompleted plus
// the first-wave metrics row (nil clears the column to NULL). Best-effort — the run's usefulness
// is the generated tests, and a metrics write failure must not turn a green run red — but it is
// SAID, because a silently unmeasured run is how measurement discipline erodes.
func completeRun(ctx context.Context, meta *metadata.Store, runID string, stable *bool, iterations *int, m *metadata.FirstWaveRunMetrics) {
	if meta == nil || strings.TrimSpace(runID) == "" {
		return
	}
	if err := meta.SetRunCompleted(ctx, runID, stable, iterations); err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: record run completion: %v\n", err)
	}
	if err := meta.SetIndexRunFirstWaveMetrics(ctx, runID, m); err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: record first-wave metrics: %v\n", err)
	}
}
