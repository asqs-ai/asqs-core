package evaluator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NoTestFilesSuffix is appended to a unit test step's Summary by the runner when the JS test runner
// exited non-zero only because it found no test files (vitest: "No test files found, exiting with
// code 1"; jest: "No tests found, exiting with code 1") and the runner therefore reported the
// step as passed.
//
// The runner cannot judge whether that is fine: it does not know which artifacts this run
// generated. The evaluator does — see overrideNoTestFilesPass — so the runner records the fact in
// the summary and leaves the verdict to the layer that has the artifact list. Exported because the
// runner package imports this one, never the other way round.
const NoTestFilesSuffix = " (no test files found; exit code ignored)"

// overrideNoTestFilesPass turns a runner-accepted "no test files found" pass back into a failure
// when this run's generated UNIT artifacts are on disk and the runner still saw nothing.
//
// Two different situations produce the same runner output, and only one of them is a pass:
//
//   - the tree genuinely has no unit tests to run — every unit artifact was discarded and only E2E
//     specs survived, or the run generated E2E only. vitest excludes e2e/** and exits 1, and run
//     api-72dad6bb281cacee338f43c48432a780 then spent three repair rounds asking the fixer to fix
//     two Playwright specs against 392 runes of "No test files found", the last of which rewrote one
//     of them. That exit code is not a failure of anything this run can repair: pass, and let the
//     E2E step that follows do the verifying.
//   - generated unit tests ARE on disk and the runner still found nothing: the include pattern or
//     the test command is wrong, and accepting the pass would ship tests nothing executed. Fail,
//     and say which files were expected to run.
func overrideNoTestFilesPass(ctx context.Context, res StepResult, opts EvalOptions, audit Auditor) StepResult {
	if !res.OK || !strings.Contains(res.Summary, NoTestFilesSuffix) {
		return res
	}
	present := generatedUnitArtifactsOnDisk(opts)
	if len(present) == 0 {
		if audit != nil {
			audit.Log(ctx, "evaluator.no_test_files_accepted", map[string]interface{}{
				"message": "The unit runner found no test files and this run has no unit artifacts on disk (E2E only, or all discarded); treating the empty unit pass as ok.",
				"step":    StepTest,
			})
		}
		return res
	}
	res.OK = false
	res.Summary = fmt.Sprintf("no test files found although %d generated unit test(s) exist on disk (%s); check the runner's include pattern and the unit test command",
		len(present), strings.Join(present, ", "))
	if audit != nil {
		audit.LogError(ctx, "evaluator.no_test_files_with_generated_tests", map[string]interface{}{
			"message":            res.Summary,
			"step":               StepTest,
			"generated_on_disk":  present,
			"remediation":        "the runner's include glob or general.build.unit_test_command does not reach the generated files",
			"runner_exit_masked": true,
		})
	}
	return res
}

// generatedUnitArtifactsOnDisk lists this run's artifact paths that are unit tests (not E2E specs)
// and currently exist under RepoPath. Discarded artifacts are gone from disk and so drop out here,
// which is exactly why the check reads the filesystem rather than the artifact list alone.
func generatedUnitArtifactsOnDisk(opts EvalOptions) []string {
	var out []string
	for _, rel := range opts.ArtifactPaths {
		rel = normalizePathForFix(rel)
		if rel == "" || pathLooksLikeE2EArtifact(rel) {
			continue
		}
		if st, err := os.Stat(filepath.Join(opts.RepoPath, filepath.FromSlash(rel))); err == nil && !st.IsDir() {
			out = append(out, rel)
		}
	}
	return out
}

// excerptRunes returns the first n runes of s, for audit payloads that quote a failure without
// carrying the whole log.
func excerptRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// IsE2EArtifactPath is pathLooksLikeE2EArtifact for callers outside the package. The workflow's
// discard resolver uses it to say which surviving artifacts only an E2E pass could have executed.
func IsE2EArtifactPath(rel string) bool { return pathLooksLikeE2EArtifact(rel) }

// pathLooksLikeE2EArtifact reports whether a repo-relative artifact path is an end-to-end spec
// rather than a unit test: anything under an e2e/, cypress/ or playwright/ directory, or whose
// file name carries a .cy. or .e2e. segment. It is the unit/E2E split the unit runner's include
// pattern makes (the bootstrap's vitest template excludes e2e/** and cypress/**), so a path that
// matches here is one the unit runner is expected NOT to see.
func pathLooksLikeE2EArtifact(rel string) bool {
	pl := strings.ToLower(filepath.ToSlash(strings.TrimSpace(rel)))
	if pl == "" {
		return false
	}
	for _, dir := range []string{"e2e/", "cypress/", "playwright/"} {
		if strings.HasPrefix(pl, dir) || strings.Contains(pl, "/"+dir) {
			return true
		}
	}
	base := filepath.Base(pl)
	return strings.Contains(base, ".cy.") || strings.Contains(base, ".e2e.")
}
