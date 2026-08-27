package pipeline

import (
	"fmt"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// usageOf seeds an accumulator the way a run does: one Add per completion.
func usageOf(prompt, completion int) *model.UsageAccumulator {
	acc := &model.UsageAccumulator{}
	acc.Add(&model.Usage{PromptTokens: prompt, CompletionTokens: completion})
	return acc
}

// The nil/row distinction is the projection's whole contract: a run whose evaluation errored (or
// never happened) must write NULL, because a row of zeroes reads as "compiled nothing, fixed
// nothing" and would drag every average in the A/B report toward zero.
func TestEvalFirstWaveMetricsForDB_errOrNilReturnsNil(t *testing.T) {
	if m := evalFirstWaveMetricsForDB(nil, nil, nil); m != nil {
		t.Fatalf("nil eval: want nil; got %+v", m)
	}
	if m := evalFirstWaveMetricsForDB(&evaluator.EvalWorkflowResult{Stable: true}, fmt.Errorf("x"), usageOf(80, 20)); m != nil {
		t.Fatalf("eval error: want nil; got %+v", m)
	}
}

func TestEvalFirstWaveMetricsForDB_mapsFields(t *testing.T) {
	eval := &evaluator.EvalWorkflowResult{
		Stable:                 true,
		Iterations:             2,
		CompileOKAfterGenerate: true,
		TestOKWithoutFix:       false,
		CompileFixCount:        1,
		TestFixCount:           1,
	}
	m := evalFirstWaveMetricsForDB(eval, nil, usageOf(1000, 234))
	if m == nil {
		t.Fatal("want a metrics row")
	}
	if !m.EvalStable || m.EvalIterations != 2 || !m.CompileOKAfterGenerate || m.TestOKWithoutFix ||
		m.CompileFixCount != 1 || m.TestFixCount != 1 || m.LlmTotalTokens != 1234 {
		t.Fatalf("mapping wrong: %+v", m)
	}
	if m.TokensToStable == nil || *m.TokensToStable != 1234 {
		t.Fatalf("TokensToStable = %v; want 1234 (stable run with tracked tokens)", m.TokensToStable)
	}
	// prompt_tokens is the prompt-side share, not the combined total — it is the ground truth the
	// context-budget counter is calibrated against, and the combined number would silently inflate
	// it by the completion side.
	if m.PromptTokens == nil || *m.PromptTokens != 1000 {
		t.Fatalf("PromptTokens = %v; want 1000 (the accumulator's prompt total)", m.PromptTokens)
	}
}

// tokens_to_stable exists only when the run ended stable AND tokens were tracked: an unstable run
// has no "cost to reach stable", and an untracked one must not report a zero cost as if free.
func TestEvalFirstWaveMetricsForDB_tokensToStableRules(t *testing.T) {
	unstable := evalFirstWaveMetricsForDB(&evaluator.EvalWorkflowResult{Stable: false}, nil, usageOf(400, 100))
	if unstable == nil || unstable.TokensToStable != nil {
		t.Fatalf("unstable run must omit tokens_to_stable: %+v", unstable)
	}
	untracked := evalFirstWaveMetricsForDB(&evaluator.EvalWorkflowResult{Stable: true}, nil, nil)
	if untracked == nil || untracked.TokensToStable != nil {
		t.Fatalf("untracked tokens must omit tokens_to_stable: %+v", untracked)
	}
	if untracked.PromptTokens != nil {
		t.Fatalf("untracked usage must omit prompt_tokens (absent ≠ zero): %+v", untracked)
	}
}

// Stability for the metric is what the pipeline reports to the operator: green after discarding
// repeatedly-failing files counts as stable, so the console and the A/B report never disagree
// about the same run.
func TestEvalFirstWaveMetricsForDB_stableAfterDiscardCountsAsStable(t *testing.T) {
	m := evalFirstWaveMetricsForDB(&evaluator.EvalWorkflowResult{
		Stable:                      false,
		EarlyExitStableAfterDiscard: true,
	}, nil, usageOf(30, 12))
	if m == nil || !m.EvalStable {
		t.Fatalf("stable-after-discard must report eval_stable: %+v", m)
	}
	if m.TokensToStable == nil || *m.TokensToStable != 42 {
		t.Fatalf("stable-after-discard with tracked tokens must set tokens_to_stable: %+v", m)
	}
}
