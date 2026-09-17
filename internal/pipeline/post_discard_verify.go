package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
//
// It also returns the survivors it discarded itself (see discardFailingSurvivors); those files are
// off disk whether or not the tree then verified, and the caller records them as discarded.
func verifyAfterDiscard(ctx context.Context, sandbox evaluator.SandboxRunner, opts evaluator.EvalOptions, discarded []string, audit evaluator.Auditor) (bool, []string) {
	opts.MaxFixIterations = PostDiscardRepairBudget
	opts.RepeatedTestFailureThreshold = -1
	opts.ArtifactPaths = withoutDiscarded(opts.ArtifactPaths, discarded)
	opts.ExtendedArtifactPaths = withoutDiscarded(opts.ExtendedArtifactPaths, discarded)
	// The caller has just removed the discarded files, so testMatch no longer describes what is on
	// disk; derive it again before this verification loads anything.
	refreshPlaywrightConfigAfterDiscard(ctx, opts.RepoPath, opts.Lang, opts.E2EFramework, audit)

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
	if ok {
		return true, nil
	}
	// Repair is over. A failure that attributes to a strict subset of the survivors gets the
	// answer the first discard gave: those artifacts go, and the remainder is verified once, with
	// no repair.
	extra := discardFailingSurvivors(ctx, opts, res.StepResults, audit)
	if len(extra) > 0 {
		// The spec set on disk just changed; testMatch has to be derived again before the
		// verification loads anything.
		refreshPlaywrightConfigAfterDiscard(ctx, opts.RepoPath, opts.Lang, opts.E2EFramework, audit)
		opts.ArtifactPaths = withoutDiscarded(opts.ArtifactPaths, extra)
		opts.ExtendedArtifactPaths = withoutDiscarded(opts.ExtendedArtifactPaths, extra)
		opts.Fixer = nil
		opts.MaxFixIterations = 1
		fmt.Fprintf(os.Stderr, "asqs-core: discarded %d surviving test file(s) the verification still attributed; verifying the remaining %d once more…\n", len(extra), len(opts.ArtifactPaths))
		again, aerr := evaluator.RunEvaluation(ctx, sandbox, opts, audit)
		ok = aerr == nil && again.Stable
		if audit != nil {
			payload := map[string]interface{}{
				"stable":             ok,
				"survivor_discarded": extra,
				"steps":              evaluator.StepSummary(again.StepResults),
			}
			if aerr != nil {
				payload["error"] = aerr.Error()
			}
			if ok {
				payload["message"] = fmt.Sprintf("Post-discard verification passed after discarding %d surviving artifact(s).", len(extra))
				audit.Log(ctx, "pipeline.post_discard_verification_pass", payload)
			} else {
				payload["message"] = fmt.Sprintf("Post-discard verification failed again after discarding %d surviving artifact(s); the run is not stable and must not ship.", len(extra))
				audit.LogError(ctx, "pipeline.post_discard_verification_fail", payload)
			}
		}
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "asqs-core: post-discard verification failed; the run is not stable.")
	}
	return ok, extra
}

// discardFailingSurvivors removes from disk the surviving artifacts the verification's last
// failure attributes, when they are a strict, non-empty subset of the survivors. Returns the
// paths removed.
//
// The asqs-go run of 2026-09-08 (api-a716cb7b25db5a880b4675a04b0b08c3): the unit discard went
// green, the never-executed E2E survivors failed every repair round on a failure no spec edit could
// fix, and six healthy unit files shipped nothing because the run was reported unstable. Those
// survivors were the case the first discard exists for, one step later.
//
// The same gates as the first tier: the failing step must be one whose output names files (test,
// E2E, compile); extended files are the repository's own and discard is os.Remove, so they are
// never removed; and a failure naming every survivor leaves nothing to keep, so nothing is removed.
func discardFailingSurvivors(ctx context.Context, opts evaluator.EvalOptions, results []evaluator.StepResult, audit evaluator.Auditor) []string {
	var failing evaluator.StepResult
	found := false
	for i := len(results) - 1; i >= 0 && !found; i-- {
		if results[i].OK {
			continue
		}
		switch results[i].Step {
		case evaluator.StepTest, evaluator.StepTestE2E, evaluator.StepCompile:
			failing, found = results[i], true
		default:
			return nil
		}
	}
	if !found || strings.TrimSpace(opts.RepoPath) == "" {
		return nil
	}
	survivors := uniqueNormPaths(opts.ArtifactPaths)
	if len(survivors) == 0 {
		return nil
	}
	extended := make(map[string]bool, len(opts.ExtendedArtifactPaths))
	for _, p := range opts.ExtendedArtifactPaths {
		extended[normPath(p)] = true
	}
	attributed := uniqueNormPaths(evaluator.ParseFailingTestPaths(failing.Output, survivors))
	var discard, protected []string
	for _, p := range attributed {
		if extended[p] {
			protected = append(protected, p)
			continue
		}
		discard = append(discard, p)
	}
	if len(discard) == 0 || len(attributed) >= len(survivors) {
		if audit != nil {
			why := "the failure output attributes no discardable surviving artifact"
			if len(attributed) >= len(survivors) {
				why = "every surviving artifact is failing, so there is nothing to keep"
			}
			audit.Log(ctx, "pipeline.post_discard_survivors_kept", map[string]interface{}{
				"message":            fmt.Sprintf("No survivor discard after step %s failed: %s.", failing.Step, why),
				"step":               string(failing.Step),
				"survivors":          survivors,
				"attributed":         attributed,
				"protected_extended": protected,
			})
		}
		return nil
	}
	var removed []string
	for _, p := range discard {
		if err := os.Remove(filepath.Join(opts.RepoPath, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "asqs-core: discard %s: %v\n", p, err)
			continue
		}
		removed = append(removed, p)
	}
	if len(removed) > 0 && audit != nil {
		audit.Log(ctx, "pipeline.post_discard_survivors_discarded", map[string]interface{}{
			"message": fmt.Sprintf("Post-discard repair is exhausted and step %s still attributes %d of %d surviving artifact(s); discarding them and verifying the rest once more (no repair).",
				failing.Step, len(removed), len(survivors)),
			"step":               string(failing.Step),
			"paths":              removed,
			"survivors":          len(survivors),
			"protected_extended": protected,
		})
	}
	return removed
}

// uniqueNormPaths normalizes paths and drops duplicates, order kept.
func uniqueNormPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		n := normPath(p)
		if n == "" || n == "." || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
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
