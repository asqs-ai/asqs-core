package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// PostDiscardRepairBudget bounds the repair rounds the post-discard verification may spend.
//
// Small, and deliberately not the run budget. A discard removes the artifacts the fix loop could
// not repair, which unblocks the steps those artifacts were masking — so this verification is the
// first time a LATER step runs at all, and what it finds is a failure class the fixer has never been
// shown. It deserves rounds; it does not deserve a second unbounded loop, because the run has by
// then already spent its whole budget.
//
// Three is enough for the shape this repairs: a handful of assertion mismatches across the artifacts
// that survived the discard. A tree needing more than that is not one round from green, and
// reporting it unstable is the honest outcome.
const PostDiscardRepairBudget = 3

// verifyAfterDiscard re-runs the whole evaluation once the discarded files are off disk, reporting
// whether the tree that remains actually passes.
//
// Without it, "stable after discard" is an inference from a suite that was never re-run: the
// evaluator stops at the first failing step, so a run whose unit tests never went green has never
// executed its E2E pass, and the discard that fixes the unit suite is precisely what would let the
// E2E pass run for the first time. The core run of 2026-09-02 shipped three generated Playwright
// specs that no step had ever executed.
//
// Two deviations from the main evaluation, both deliberate:
//
//   - discard is disabled (a negative RepeatedTestFailureThreshold). A verification that answers a
//     failure by deleting more generated output is not a verification, and each round of it erodes
//     the run's own result with no bound on how much.
//   - the discarded paths leave the writable set. They are gone from disk; leaving them in invites
//     the fixer to "repair" a file that is not there.
func verifyAfterDiscard(ctx context.Context, sandbox evaluator.SandboxRunner, opts evaluator.EvalOptions, discarded []string, audit evaluator.Auditor) bool {
	opts.MaxFixIterations = PostDiscardRepairBudget
	opts.RepeatedTestFailureThreshold = -1
	opts.ArtifactPaths = withoutDiscarded(opts.ArtifactPaths, discarded)
	opts.ExtendedArtifactPaths = withoutDiscarded(opts.ExtendedArtifactPaths, discarded)

	if audit != nil {
		audit.Log(ctx, "pipeline.post_discard_verification_start", map[string]interface{}{
			"message":       fmt.Sprintf("Re-evaluating the project after discarding %d artifact(s); steps the discard unblocked run here for the first time.", len(discarded)),
			"discarded":     len(discarded),
			"remaining":     len(opts.ArtifactPaths),
			"repair_budget": PostDiscardRepairBudget,
		})
	}
	fmt.Fprintf(os.Stderr, "asqs-core: verifying the %d test file(s) that survived the discard…\n", len(opts.ArtifactPaths))

	res, err := evaluator.RunEvaluation(ctx, sandbox, opts, audit)
	ok := err == nil && res.Stable
	if audit != nil {
		payload := map[string]interface{}{
			"stable":     ok,
			"iterations": res.Iterations,
			"steps":      evaluator.StepSummary(res.StepResults),
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		if ok {
			payload["message"] = fmt.Sprintf("Post-discard verification passed after %d iteration(s).", res.Iterations)
			audit.Log(ctx, "pipeline.post_discard_verification_pass", payload)
		} else {
			payload["message"] = fmt.Sprintf(
				"Post-discard verification failed after %d iteration(s) of %d; the run is not stable and must not ship.",
				res.Iterations, PostDiscardRepairBudget)
			audit.LogError(ctx, "pipeline.post_discard_verification_fail", payload)
		}
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "asqs-core: post-discard verification failed; the run is not stable.")
	}
	return ok
}

// withoutDiscarded removes the discarded paths from an artifact list, comparing on the same
// normalized form the discard itself used.
func withoutDiscarded(paths, discarded []string) []string {
	if len(paths) == 0 || len(discarded) == 0 {
		return paths
	}
	gone := make(map[string]bool, len(discarded))
	for _, d := range discarded {
		gone[normPath(strings.TrimSpace(d))] = true
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if gone[normPath(strings.TrimSpace(p))] {
			continue
		}
		out = append(out, p)
	}
	return out
}
