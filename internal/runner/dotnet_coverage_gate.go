package runner

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// coverletPackages are the two packages that make `dotnet test --collect "XPlat Code Coverage"`
// actually produce a report. coverlet.collector registers the data collector the --collect flag
// names; coverlet.msbuild is the MSBuild-integrated alternative.
var coverletPackages = []string{"coverlet.collector", "coverlet.msbuild"}

const maxCoverageProbeWalkDepth = 12

// dotnetCoverageCollectorDeclared reports whether any project in the repo references coverlet, and
// which package it found.
//
// Without it the --collect flag is inert: VSTest has no collector registered under that name, so
// the coverage step re-runs the entire suite and produces nothing. Java has had the mirror-image
// gate since JaCoCo (no plugin declared, no coverage step); C# had none, so every C# run paid for a
// second full test execution to report "coverage report not found".
//
// The scan goes through dotnetproj.ResolveFacts so a reference whose version lives in a central
// Directory.Packages.props still counts — that is the shape a modern solution has.
func dotnetCoverageCollectorDeclared(repoRoot string) (string, bool) {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	if root == "" {
		return "", false
	}
	found := ""
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxCoverageProbeWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		facts, ferr := dotnetproj.ResolveFacts(root, path)
		if ferr != nil {
			return nil
		}
		if pkg, ok := facts.ReferencesAnyPackage(coverletPackages...); ok {
			found = canonicalCoverletName(pkg)
		}
		return nil
	})
	return found, found != ""
}

// canonicalCoverletName restores the package's documented casing; facts key on the lower-cased id
// because NuGet ids are case-insensitive.
func canonicalCoverletName(id string) string {
	for _, p := range coverletPackages {
		if strings.EqualFold(p, id) {
			return p
		}
	}
	return id
}

// dotnetCoverageSkipReason is what the step reports when the gate closes. It names the package
// because adding it is the operator's fix; bootstrap deliberately does not add it for them (D4),
// since that would mean editing a project file to satisfy a reporting step.
func dotnetCoverageSkipReason() string {
	return "skip (no coverlet.collector or coverlet.msbuild referenced by any project; " +
		"`dotnet test --collect \"XPlat Code Coverage\"` produces no report without it)"
}

// dotnetCoverageResultsDirArgv appends --results-directory so every test project writes its
// TestResults under one known root instead of beside each project, which is what made the report
// unfindable from the eval cwd. No-op when the argv already names one.
func dotnetCoverageResultsDirArgv(argv []string, resultsDir string) []string {
	if strings.TrimSpace(resultsDir) == "" || len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	for _, a := range argv {
		if strings.EqualFold(a, "--results-directory") || strings.EqualFold(a, "-r") {
			return argv
		}
	}
	return append(append([]string(nil), argv...), "--results-directory", resultsDir)
}

// dotnetCoverageResultsDir is the single directory every test project writes its TestResults into.
// Relative to the eval cwd, so it is valid inside the container as well as on the host.
func dotnetCoverageResultsDir(absGitRoot, absCwd string) string {
	rel, err := filepath.Rel(absCwd, filepath.Join(absGitRoot, "TestResults"))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "TestResults"
	}
	return filepath.ToSlash(rel)
}
