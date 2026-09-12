package layout

import (
	"path/filepath"
	"regexp"
	"strings"
)

// csharpTestDirSegment matches a path segment that names a test tree by itself.
//
// `.Tests`-style suffixes are how .NET names a test PROJECT (Shop.Tests, Shop.UnitTests,
// Shop.IntegrationTests, Shop.Specs), and the segment is both the directory and the assembly name.
var csharpTestDirSegment = regexp.MustCompile(`(?i)^(?:tests?|testing|e2e|unittests?|integrationtests?|specs?)$|\.(?:tests?|unittests?|integrationtests?|specs?|e2e)$`)

// IsCSharpTestPath reports whether a repo-relative path is a C# test file.
//
// It exists because three predicates answered this question and disagreed. fix_write_path.go
// carried the full rule; workflow/postgenerate_write.go and orchestrator/workflow.go both used
// `strings.Contains(base, "tests")`, which rejects FooTest.cs — the MSTest and NUnit convention —
// so a correctly named generated test could be written by one gate and refused by the next, and
// `Contest.cs` and `LatestOrder.cs` were accepted by all three.
//
// Two independent signals, either of which is enough:
//
//   - the FILE is named for a test: `*Tests.cs` or `*Test.cs`, but not exactly `Test.cs` or
//     `Tests.cs`, which name nothing and are far likelier to be a production type; and
//   - a path SEGMENT names a test tree, which covers the fixtures, builders and helpers that sit
//     beside the tests under the same project and carry no naming convention at all.
func IsCSharpTestPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	// Windows separators arrive from a Windows runner and from MSBuild diagnostics alike.
	p := strings.ReplaceAll(filepath.ToSlash(path), `\`, "/")
	if !strings.EqualFold(filepath.Ext(p), ".cs") {
		return false
	}

	// The suffix is matched in its ORIGINAL casing, because the capital is the word boundary.
	// Lower-casing first made every stem ending in the letters t-e-s-t a test: `Contest.cs` and
	// `Protest.cs` are production types, and C# names types in PascalCase, so `BasketTests` has a
	// capital T where `Contest` does not.
	stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	switch {
	case stem == "Test" || stem == "Tests":
		// Named for nothing in particular; the segment rules below still get a say.
	case strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "Test"):
		return true
	case strings.HasSuffix(stem, "E2E"):
		return true
	}

	for _, seg := range strings.Split(filepath.Dir(p), "/") {
		if csharpTestDirSegment.MatchString(seg) {
			return true
		}
	}
	return false
}
