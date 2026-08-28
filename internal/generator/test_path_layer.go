package generator

import (
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/layout"
)

// Layer-aware test path classification.
//
// A test artifact's LAYER (unit vs e2e) is part of its identity, not a detail of its content. A
// Spring @WebMvcTest slice and a @SpringBootTest end-to-end suite need different runtime wiring,
// different fixtures, and often a different Maven/Gradle execution phase — merging one into the
// other produces a file that cannot load a context no matter how correct each individual test is.
//
// This mattered because ExistingOrSuggestedTestPath redirects a gap onto an existing on-disk test
// file, and the candidate list it draws from (RetrievalContext.ExistingTestPaths) is keyed on the
// SOURCE file, which both layers share. createTestPlanFromGaps populates that list identically for
// the unit and e2e plan layers, so an API_ROUTE e2e gap on OwnerController.java saw
// src/test/java/.../OwnerControllerTests.java — the unit suite — and redirected into it, silently
// discarding the E2EIT path the e2e suggester had produced.

// e2eFileNameMarkers are filename fragments that identify an end-to-end artifact across the
// languages asqs generates for. Matched case-insensitively against the base name.
//
//   - Java:  FooE2EIT.java (suggestedJavaE2EPathForRouteGap), and the Maven Failsafe *IT.java
//     convention handled separately by hasJavaITSuffix.
//   - C#:    FooE2ETests.cs (layout.SuggestedCSharpE2ETestPath).
//   - JS/TS: foo.e2e-spec.ts (Playwright), foo.cy.ts (Cypress), foo.e2e.ts.
var e2eFileNameMarkers = []string{
	"e2eit",
	"e2etests",
	"e2etest",
	".e2e-spec.",
	".e2e.",
	".cy.",
}

// e2eDirSegments are path segments that place a file in a dedicated end-to-end tree.
var e2eDirSegments = []string{"e2e", "e2e-tests", "e2e_tests", "endtoend", "cypress", "playwright"}

// IsE2ETestPath reports whether a repo-relative test path belongs to the end-to-end layer.
//
// Deliberately conservative in one direction: "integration"/"IntegrationTests" appear in
// layout.E2ERootDirCandidates because they are plausible homes for a NEW e2e artifact, but plenty
// of repos put ordinary Spring slice tests there too. Treating those directories as proof of layer
// would misclassify existing unit suites, so directory evidence is limited to unambiguous segments
// and the filename markers carry the rest.
func IsE2ETestPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(strings.TrimSpace(p)))
	if p == "" {
		return false
	}
	base := filepath.Base(p)
	for _, m := range e2eFileNameMarkers {
		if strings.Contains(base, m) {
			return true
		}
	}
	if hasJavaITSuffix(base) {
		return true
	}
	for _, seg := range strings.Split(filepath.Dir(p), "/") {
		for _, cand := range e2eDirSegments {
			if seg == cand {
				return true
			}
		}
	}
	return false
}

// hasJavaITSuffix matches the Maven Failsafe integration-test convention (FooIT.java), which runs
// in a different lifecycle phase from Surefire unit tests. Requires a preceding lowercase letter or
// digit so an all-caps acronym class (e.g. HTTPIT is fine, but "IT.java" alone is not) does not
// match on its own.
func hasJavaITSuffix(base string) bool {
	if !strings.HasSuffix(base, "it.java") {
		return false
	}
	stem := strings.TrimSuffix(base, ".java")
	return len(stem) > 2
}

// filterExistingTestPathsByLayer keeps only the candidates whose layer matches the item being
// generated. Returns the filtered list plus the paths dropped, so the caller can audit a redirect
// that was refused rather than have it vanish.
//
// An empty result is the correct and expected outcome when a repo has unit tests but no e2e suite:
// the caller then falls back to the layer's own suggested default, which is precisely the
// behaviour that was missing.
func filterExistingTestPathsByLayer(paths []string, wantE2E bool) (kept, dropped []string) {
	for _, p := range paths {
		if IsE2ETestPath(p) == wantE2E {
			kept = append(kept, p)
			continue
		}
		dropped = append(dropped, p)
	}
	return kept, dropped
}

// existingTestPathsForItem returns the on-disk test candidates that a given plan item may legally
// redirect into, along with any it was refused.
func existingTestPathsForItem(item *retrieval.TestPlanItem) (kept, dropped []string) {
	if item == nil || item.Context == nil || len(item.Context.ExistingTestPaths) == 0 {
		return nil, nil
	}
	return filterExistingTestPathsByLayer(item.Context.ExistingTestPaths, retrieval.IsE2EPlanItem(item))
}

// dedicatedRootDirCandidatesForLang exposes the layout package's root list to the canonical-path
// picker without importing layout at every call site.
func dedicatedRootDirCandidatesForLang() []string { return layout.DedicatedRootDirCandidates }
