package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/dotnetproj"
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
//
// A glob that does not match at the configured location is retried as a suffix anywhere in the tree.
// This is what makes the dotnet pattern work: `dotnet test --collect "XPlat Code Coverage"` writes
// <test project>/TestResults/<random guid>/coverage.cobertura.xml, which is neither at the eval cwd
// nor at a fixed depth, and filepath.Glob's single-level `*` could only ever find it in the one
// layout where the eval cwd happened to be the test project itself. Every other C# run reported
// "coverage report not found" while the report sat two directories away.
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
		if err == nil {
			if best := newestReport(keepRegularFiles(matches)); best != "" {
				if r, rerr := filepath.Rel(root, best); rerr == nil {
					return filepath.ToSlash(r)
				}
			}
		}
		if found := findCoverageReportBySuffix(root, rel); found != "" {
			return found
		}
	}
	return ""
}

func keepRegularFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// maxCoverageReportWalkDepth bounds the fallback walk. Reports live a handful of directories below
// a project; anything deeper is not the build output of this repository.
const maxCoverageReportWalkDepth = 10

// findCoverageReportBySuffix walks the repo for a file whose tail matches the glob pattern, and
// returns the lexicographically first match so two test projects cannot make the result depend on
// directory order. Build-output trees are skipped except the one the pattern itself names.
func findCoverageReportBySuffix(root, pattern string) string {
	pattern = filepath.ToSlash(strings.TrimPrefix(pattern, "./"))
	if pattern == "" {
		return ""
	}
	first := strings.SplitN(pattern, "/", 2)[0]
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			// The pattern's own first segment (TestResults, target, …) is where reports live, so it
			// is never pruned even when it looks like build output.
			if !strings.EqualFold(d.Name(), first) &&
				(dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCoverageReportWalkDepth) {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if coveragePathMatchesSuffix(filepath.ToSlash(rel), pattern) {
			matches = append(matches, path) // absolute: newestReport stats these
		}
		return nil
	})
	best := newestReport(matches)
	if best == "" {
		return ""
	}
	rel, err := filepath.Rel(root, best)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// newestReport picks the most recently written file, breaking ties by path.
//
// `dotnet test --collect` writes a fresh TestResults/<random guid>/coverage.cobertura.xml on every
// invocation and never removes the previous one, so ordering by name orders by GUID — an arbitrary
// run. In a fix loop that runs coverage more than once, or a workspace reused between runs, that
// could name a report from an earlier iteration while reporting itself as deterministic.
func newestReport(paths []string) string {
	best, bestMod := "", time.Time{}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		mod := st.ModTime()
		if best == "" || mod.After(bestMod) || (mod.Equal(bestMod) && p < best) {
			best, bestMod = p, mod
		}
	}
	return best
}

// coveragePathMatchesSuffix reports whether rel ends with a path matching pattern, segment-aligned.
func coveragePathMatchesSuffix(rel, pattern string) bool {
	relSegs := strings.Split(rel, "/")
	patSegs := strings.Split(pattern, "/")
	if len(relSegs) < len(patSegs) {
		return false
	}
	tail := relSegs[len(relSegs)-len(patSegs):]
	for i, p := range patSegs {
		ok, err := filepath.Match(p, tail[i])
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// coverageSummaryFromPlan builds the coverage step's summary for either target.
func coverageSummaryFromPlan(repoPath string, plan StepPlan) string {
	if rel := findCoverageReport(repoPath, plan.CoverageReportPaths); rel != "" {
		return "coverage report: " + rel
	}
	return "tests ok (coverage report not found)"
}
