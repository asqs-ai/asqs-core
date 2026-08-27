package generator

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// Test-naming convention detection.
//
// The generator's built-in default for a Java unit test is `FooTest.java`. Plenty of repositories —
// spring-petclinic among them — use `FooTests.java`. When the default disagrees with the repository,
// every run creates a sibling next to the file it should have extended, and once both exist the
// redirect picks between them on sort order: `Test.java` precedes `Tests.java` because '.' (0x2E)
// sorts before 's' (0x73), so the tool's own leftover shadows the repository's real suite forever.
// That is not hypothetical — it is what run api-d7e0cbece3e9260f73836f5d50d21c96 did to
// OwnerControllerTests.java, and it left the workspace carrying BOTH files for two different SUTs.
//
// Detecting the convention fixes it at the source: on a `*Tests` repository the default path for a
// brand-new artifact becomes `FooTests.java`, so a new artifact and an existing one collide by
// construction and resolve to "extend" through machinery that already works. Fixing only the
// tie-break would leave the gap open for any SUT that has no test yet.
//
// The vote excludes ASQS-authored files (see genmanifest). It has to: on the fixture repo the
// upstream signal was 14 `*Tests.java` to 1 `*Test.java` (93%), but after one ASQS run committed
// seven `*Test.java` files it read 14 to 9 (61%) — one more run and the tool would have voted its
// own mistake into the house style.

// TestSuffixConvention is the detected naming convention for a repository's UNIT test artifacts.
// The zero value means "not detected"; callers keep their built-in default.
type TestSuffixConvention struct {
	// Suffix is the language-appropriate token: Java/C# "Test" or "Tests"; JS/TS ".test." or
	// ".spec.". Empty when undetected or ambiguous.
	Suffix string
	// Samples is how many human-authored test files were counted.
	Samples int
	// Share is the winner's fraction of Samples, 0..1.
	Share float64
	// Ambiguous is true when files were found but no option cleared the thresholds. Distinct from
	// "no samples": it is worth auditing, because it usually means a repo genuinely mixes styles.
	Ambiguous bool
	// ExcludedGenerated is how many files were left out of the vote as ASQS-authored.
	ExcludedGenerated int
}

// Detected reports whether the convention is usable.
func (c TestSuffixConvention) Detected() bool { return strings.TrimSpace(c.Suffix) != "" }

// Describe renders the decision for an audit payload.
func (c TestSuffixConvention) Describe() string {
	switch {
	case c.Detected():
		return fmt.Sprintf("%s (%.0f%% of %d human-authored test file(s); %d ASQS-authored excluded)",
			c.Suffix, c.Share*100, c.Samples, c.ExcludedGenerated)
	case c.Ambiguous:
		return fmt.Sprintf("ambiguous across %d human-authored test file(s); keeping the built-in default", c.Samples)
	default:
		return "no human-authored test files to learn from; keeping the built-in default"
	}
}

const (
	// minConventionSamples is the smallest corpus worth learning from. Two files agreeing is a
	// coincidence; the built-in default is a better bet than a 2-sample majority.
	minConventionSamples = 3
	// minConventionShare is the fraction the winner must reach. 0.6 keeps the fixture repo's
	// post-pollution 61% reading correct while refusing a near-even split, where following either
	// option would be guessing.
	minConventionShare = 0.6
)

// DetectTestSuffixConvention infers the repository's unit-test naming convention.
//
// generated is the provenance set (may be nil / empty). E2E artifacts are excluded from the vote
// entirely: they have their own suffix (`*E2EIT.java`, `*.e2e-spec.ts`) chosen by the e2e
// suggester, and counting them would pull the unit default toward a layer it does not apply to.
func DetectTestSuffixConvention(files []indexer.FileVersion, lang string, generated genmanifest.Set) TestSuffixConvention {
	counts := map[string]int{}
	total := 0
	excluded := 0
	for _, fv := range files {
		p := genmanifest.Normalize(fv.Path)
		if p == "" || IsE2ETestPath(p) {
			continue
		}
		suffix := unitTestSuffixOf(p, lang)
		if suffix == "" {
			continue
		}
		if generated.Has(p) {
			excluded++
			continue
		}
		counts[suffix]++
		total++
	}
	if total == 0 {
		return TestSuffixConvention{ExcludedGenerated: excluded}
	}
	// Deterministic winner: highest count, ties broken by suffix name so the result never depends
	// on map iteration order.
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bestN := "", 0
	for _, k := range keys {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	share := float64(bestN) / float64(total)
	if total < minConventionSamples || share < minConventionShare {
		return TestSuffixConvention{Samples: total, Share: share, Ambiguous: true, ExcludedGenerated: excluded}
	}
	return TestSuffixConvention{Suffix: best, Samples: total, Share: share, ExcludedGenerated: excluded}
}

// unitTestSuffixOf classifies one path into a unit-test suffix token, or "" when it is not a unit
// test artifact for lang.
func unitTestSuffixOf(rel, lang string) string {
	base := filepath.Base(rel)
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java":
		if !strings.HasSuffix(base, ".java") {
			return ""
		}
		if !strings.Contains(rel, "src/test/") && !strings.Contains(rel, "src/it/") {
			return ""
		}
		stem := strings.TrimSuffix(base, ".java")
		// Order matters: "FooTests" ends with "Test" too.
		if strings.HasSuffix(stem, "Tests") {
			return "Tests"
		}
		if strings.HasSuffix(stem, "Test") {
			return "Test"
		}
		return ""
	case "csharp", "cs":
		if !strings.HasSuffix(strings.ToLower(base), ".cs") {
			return ""
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if strings.HasSuffix(stem, "Tests") {
			return "Tests"
		}
		if strings.HasSuffix(stem, "Test") {
			return "Test"
		}
		return ""
	case "javascript", "typescript", "js", "ts":
		lb := strings.ToLower(base)
		if strings.Contains(lb, ".spec.") {
			return ".spec."
		}
		if strings.Contains(lb, ".test.") {
			return ".test."
		}
		return ""
	}
	return ""
}

// conventionForItem recovers the convention the plan phase attached to an item. Only Suffix
// survives the trip — the sample counts are audit detail, not decision input — so the reconstructed
// value is Detected() exactly when a suffix was carried.
func conventionForItem(item *retrieval.TestPlanItem) TestSuffixConvention {
	if item == nil || item.Context == nil {
		return TestSuffixConvention{}
	}
	return TestSuffixConvention{Suffix: strings.TrimSpace(item.Context.TestSuffixConvention)}
}

// applyUnitSuffixConvention rewrites a suggested unit-test path to the repository's convention.
// Returns path unchanged when the convention is undetected, already matches, or does not apply to
// the path's shape. Never touches an e2e artifact.
func applyUnitSuffixConvention(path string, lang string, conv TestSuffixConvention) string {
	if !conv.Detected() || strings.TrimSpace(path) == "" || IsE2ETestPath(path) {
		return path
	}
	current := unitTestSuffixOf(path, lang)
	if current == "" || current == conv.Suffix {
		return path
	}
	dir := filepath.ToSlash(filepath.Dir(path))
	base := filepath.Base(path)
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java", "csharp", "cs":
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		stem = strings.TrimSuffix(stem, current)
		if stem == "" {
			return path
		}
		return joinRel(dir, stem+conv.Suffix+ext)
	case "javascript", "typescript", "js", "ts":
		return joinRel(dir, strings.Replace(base, current, conv.Suffix, 1))
	}
	return path
}

// joinRel joins a forward-slashed dir and base without introducing a leading "./".
func joinRel(dir, base string) string {
	if dir == "" || dir == "." {
		return base
	}
	return dir + "/" + base
}
