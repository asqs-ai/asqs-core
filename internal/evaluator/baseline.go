package evaluator

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// Baseline failure classification.
//
// The run this was built for never established that the tree was already red before it generated
// anything. It spent 8 minutes generating and 45 fixing on a baseline that could not compile at
// minute zero, and its audit attributed every failure to itself. The workspace HEAD was a prior
// QualityBot commit, and every file that stalled the loop came from it:
//
//	$ git log --diff-filter=A -1 -- .../PetClinicRuntimeHintsTest.java
//	9e6e77d QualityBot Run (asqs[api-03cb14d84b13f9571368eda95152bddf]) …
//
// The failing lines in OwnerTest.java (119, 126, 137) and OwnerControllerTest.java (122, 188) were
// baseline lines: this run's diff hunks start at @@ -140 and @@ -160 with ZERO deletions.
//
// This does NOT make a red baseline a hard stop. Repairing inherited breakage is the product. What
// it adds is the signal that makes the repair deliberate rather than accidental, and the run's
// report honest: a run that fixed four inherited failures and introduced none is a success, and
// currently reports as fix_loop_stalled.

// BaselineFailures is the compile state of the tree BEFORE this run generated anything.
type BaselineFailures struct {
	// Captured is false when no baseline compile ran (no sandbox, no repo, or the step was
	// skipped). Callers must treat "not captured" as "cannot classify" rather than as "clean".
	Captured bool
	// Clean is true when the baseline compiled.
	Clean bool
	// Signature is the position-insensitive failure signature of the baseline output, so a later
	// identical failure is recognisable across line-number drift.
	Signature string
	// Paths are the repo-relative files the baseline diagnostic blamed, sorted.
	Paths []string
	// Summary is a short excerpt for audit.
	Summary string
}

// Inherited reports whether path was already failing before this run started.
func (b BaselineFailures) Inherited(path string) bool {
	if !b.Captured {
		return false
	}
	n := normalizeRel(path)
	for _, p := range b.Paths {
		if p == n {
			return true
		}
	}
	return false
}

func normalizeRel(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "/")
}

// CaptureBaselineFailures compiles the tree once, before generation, and records what was already
// broken.
//
// Reuses the run's own SandboxRunner and EvalOptions, so it goes through the same Docker seam and
// image the evaluation will use — a second toolchain path would be free to disagree with the one
// whose verdict actually matters. One invocation per run, not per fix round.
func CaptureBaselineFailures(ctx context.Context, runner SandboxRunner, in EvalOptions) BaselineFailures {
	if runner == nil {
		return BaselineFailures{}
	}
	repo := strings.TrimSpace(in.RepoPath)
	if repo == "" {
		return BaselineFailures{}
	}
	opts := in
	opts.RepoPath = repo
	// No artifacts exist yet, and the fixer must never run here: this is an observation, not a
	// repair.
	opts.ArtifactPaths = nil
	opts.Fixer = nil

	res := RunCompile(ctx, runner, opts)
	if res.OK {
		return BaselineFailures{Captured: true, Clean: true}
	}
	cited := errout.AllCitedRepoPaths(res.Output, filepath.Clean(repo))
	paths := make([]string, 0, len(cited))
	seen := map[string]bool{}
	for _, p := range cited {
		n := normalizeRel(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		paths = append(paths, n)
	}
	sort.Strings(paths)
	return BaselineFailures{
		Captured:  true,
		Clean:     false,
		Signature: FailureSignature(opts.Lang, StepCompile, res.Output),
		Paths:     paths,
		Summary:   firstLines(res.Output, 3),
	}
}

// ClassifyFailures splits the paths a later diagnostic blames into those the baseline already had
// and those this run introduced.
func ClassifyFailures(baseline BaselineFailures, errorOutput, repoPath string) (inherited, introduced []string) {
	if strings.TrimSpace(errorOutput) == "" {
		return nil, nil
	}
	cited := errout.AllCitedRepoPaths(errorOutput, filepath.Clean(repoPath))
	seen := map[string]bool{}
	for _, p := range cited {
		n := normalizeRel(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		if baseline.Inherited(n) {
			inherited = append(inherited, n)
			continue
		}
		introduced = append(introduced, n)
	}
	sort.Strings(inherited)
	sort.Strings(introduced)
	return inherited, introduced
}

// BaselineProgress describes what a finished run did to the inherited failure set. Used to decide
// whether rescheduling can plausibly help.
type BaselineProgress struct {
	// BaselineCount is how many files were already failing before the run.
	BaselineCount int
	// StillFailing is how many of those are still failing at the end.
	StillFailing int
	// Introduced is how many newly-broken files the run added.
	Introduced int
	// Known is false when no baseline was captured, in which case no progress claim can be made.
	Known bool
}

// Improved reports whether the run strictly shrank the inherited failure set.
func (p BaselineProgress) Improved() bool {
	return p.Known && p.BaselineCount > 0 && p.StillFailing < p.BaselineCount
}

// Describe renders the outcome for audit and for the human-in-the-loop email.
func (p BaselineProgress) Describe() string {
	if !p.Known {
		return "no baseline was captured, so inherited and introduced failures cannot be separated"
	}
	if p.BaselineCount == 0 {
		if p.Introduced == 0 {
			return "the tree compiled before the run and still does"
		}
		return fmt.Sprintf("the tree compiled before the run; this run introduced %d failing file(s)", p.Introduced)
	}
	return fmt.Sprintf("%d of %d inherited failing file(s) remain; this run introduced %d",
		p.StillFailing, p.BaselineCount, p.Introduced)
}

// EvaluateBaselineProgress compares the final failure output against the captured baseline.
func EvaluateBaselineProgress(baseline BaselineFailures, finalErrorOutput, repoPath string) BaselineProgress {
	if !baseline.Captured {
		return BaselineProgress{}
	}
	inherited, introduced := ClassifyFailures(baseline, finalErrorOutput, repoPath)
	return BaselineProgress{
		BaselineCount: len(baseline.Paths),
		StillFailing:  len(inherited),
		Introduced:    len(introduced),
		Known:         true,
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
