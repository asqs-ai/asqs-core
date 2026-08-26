package runner

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// Coverage report discovery, shared by both sandbox targets.
//
// Before this, the local target knew two hard-coded JaCoCo paths and returned a bare string for
// JS and .NET, while the Docker target reported a flat "Coverage ok" and never named a report at
// all. profile.Profiles already carried a ReportPaths table for all four ecosystems and was
// entirely unreferenced.
//
// # The glob
//
// That table's Java entry was `target/site/jacoco/*/index.html`, while the code that actually ran
// matched the fixed `target/site/jacoco/index.html`. The fixed path is the standard single-module
// JaCoCo output and is the one that has been matching; the `*` segment corresponds to no layout
// Maven produces. Treated as a bug in the unused table and corrected there, rather than teaching
// the lookup to match something that does not exist. Glob syntax is still supported by the lookup,
// because ReportPaths is a natural place to express one and silently ignoring a `*` would be the
// same class of trap.

// languageKeyForToolchain maps a toolchain onto the profile.Profiles key that describes it.
func languageKeyForToolchain(id profile.ToolchainID) string {
	switch id {
	case profile.JavaMaven, profile.JavaMaven11, profile.JavaMaven21,
		profile.JavaGradle, profile.JavaGradle11, profile.JavaGradle21:
		return "java"
	case profile.TypeScriptNPM, profile.TypeScriptPNPM, profile.TypeScriptYarn:
		return "typescript"
	case profile.CSharpDotnet:
		return "csharp"
	default:
		return ""
	}
}

// coverageReportPathsFor returns the repo-relative paths (or globs) where a coverage report is
// expected for a toolchain. One source for both targets, so a summary that names a report on one
// cannot stay silent on the other.
func coverageReportPathsFor(id profile.ToolchainID) []string {
	key := languageKeyForToolchain(id)
	if key == "" {
		return nil
	}
	lp, ok := profile.Profiles[key]
	if !ok {
		return nil
	}
	return append([]string(nil), lp.ReportPaths...)
}

// findCoverageReport returns the first report path that exists under repoPath, or "".
// Entries may be plain paths or globs; both are resolved relative to repoPath.
func findCoverageReport(repoPath string, paths []string) string {
	root := filepath.Clean(strings.TrimSpace(repoPath))
	for _, rel := range paths {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.ContainsAny(rel, "*?[") {
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				return rel
			}
			continue
		}
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		if r, rerr := filepath.Rel(root, matches[0]); rerr == nil {
			return filepath.ToSlash(r)
		}
	}
	return ""
}

// coverageSummaryFromPlan builds the coverage step's summary for either target.
func coverageSummaryFromPlan(repoPath string, plan StepPlan) string {
	if rel := findCoverageReport(repoPath, plan.CoverageReportPaths); rel != "" {
		return "coverage report: " + rel
	}
	return "tests ok (coverage report not found)"
}
