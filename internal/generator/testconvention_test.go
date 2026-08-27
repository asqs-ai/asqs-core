package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

const petclinicPkg = "src/test/java/org/springframework/samples/petclinic/owner/"

func fileVersions(paths ...string) []indexer.FileVersion {
	out := make([]indexer.FileVersion, 0, len(paths))
	for _, p := range paths {
		out = append(out, indexer.FileVersion{Path: p})
	}
	return out
}

// The tie-break that shipped was lexicographic, and "Test.java" sorts before "Tests.java" because
// '.' (0x2E) precedes 's' (0x73). Both candidates live under src/test/java, so the old canonical
// check matched both and paths[0] decided — which is how run api-d7e0cbece3e9260f73836f5d50d21c96
// extended a leftover OwnerControllerTest.java for five rounds while the repository's real
// OwnerControllerTests.java sat beside it, untouched and compiling.
func TestRankExistingTestPaths_prefersConventionOverSortOrder(t *testing.T) {
	leftover := petclinicPkg + "OwnerControllerTest.java"
	canonical := petclinicPkg + "OwnerControllerTests.java"

	// Sorted, exactly as buildExistingTestIndex hands them over.
	got := rankExistingTestPaths([]string{leftover, canonical}, "java",
		TestSuffixConvention{Suffix: "Tests"}, nil)
	if got != canonical {
		t.Errorf("ranked %s; want the repo-convention file %s", got, canonical)
	}
}

// Provenance is the tie-break when both files match the convention, or when there is no convention
// at all: ASQS must never prefer its own leftover to a human-authored suite.
func TestRankExistingTestPaths_prefersHumanAuthored(t *testing.T) {
	mine := petclinicPkg + "AaaTests.java" // sorts FIRST, so lexicographic order would pick it
	theirs := petclinicPkg + "ZzzTests.java"
	generated := genmanifest.Manifest{Entries: []genmanifest.Entry{{Path: mine}}}.AsSet()

	got := rankExistingTestPaths([]string{mine, theirs}, "java", TestSuffixConvention{Suffix: "Tests"}, generated)
	if got != theirs {
		t.Errorf("ranked ASQS-authored %s over human-authored %s", got, theirs)
	}
}

// Unknown provenance must read as human-authored, or every repo ASQS has never written to would
// have all its candidates demoted equally — and the tie-break would silently fall through to sort
// order again.
func TestRankExistingTestPaths_unknownProvenanceCountsAsHuman(t *testing.T) {
	a := petclinicPkg + "AaaTests.java"
	b := petclinicPkg + "ZzzTests.java"
	if got := rankExistingTestPaths([]string{b, a}, "java", TestSuffixConvention{Suffix: "Tests"}, nil); got != a {
		t.Errorf("with no provenance the deterministic last resort should be lexicographic; got %s want %s", got, a)
	}
}

func TestDetectTestSuffixConvention(t *testing.T) {
	// The fixture repo's pristine upstream: 14 *Tests, 1 *Test.
	var pristine []string
	for i := 0; i < 14; i++ {
		pristine = append(pristine, petclinicPkg+string(rune('A'+i))+"Tests.java")
	}
	pristine = append(pristine, petclinicPkg+"OddOneTest.java")

	conv := DetectTestSuffixConvention(fileVersions(pristine...), "java", nil)
	if conv.Suffix != "Tests" {
		t.Fatalf("pristine repo: got %q, want Tests (%s)", conv.Suffix, conv.Describe())
	}

	// After one ASQS run added 9 *Test.java files the raw signal drops to 61%. Excluding ASQS
	// authorship must restore the upstream reading — without it the tool votes its own mistake
	// into the house style within one or two more runs.
	polluted := append([]string(nil), pristine...)
	var mine []genmanifest.Entry
	for i := 0; i < 9; i++ {
		p := petclinicPkg + "Gen" + string(rune('A'+i)) + "Test.java"
		polluted = append(polluted, p)
		mine = append(mine, genmanifest.Entry{Path: p})
	}
	generated := genmanifest.Manifest{Entries: mine}.AsSet()

	if got := DetectTestSuffixConvention(fileVersions(polluted...), "java", generated); got.Suffix != "Tests" {
		t.Errorf("with provenance: got %q, want Tests (%s)", got.Suffix, got.Describe())
	}
	if got := DetectTestSuffixConvention(fileVersions(polluted...), "java", nil); got.ExcludedGenerated != 0 {
		t.Errorf("nil provenance must exclude nothing; excluded %d", got.ExcludedGenerated)
	}
}

func TestDetectTestSuffixConvention_refusesWeakEvidence(t *testing.T) {
	// Two files agreeing is a coincidence, not a convention.
	tiny := DetectTestSuffixConvention(fileVersions(petclinicPkg+"ATests.java", petclinicPkg+"BTests.java"), "java", nil)
	if tiny.Detected() {
		t.Errorf("2 samples should not yield a convention; got %q", tiny.Suffix)
	}
	// A near-even split is a mixed repo; following either option would be guessing.
	even := DetectTestSuffixConvention(fileVersions(
		petclinicPkg+"ATests.java", petclinicPkg+"BTests.java",
		petclinicPkg+"CTest.java", petclinicPkg+"DTest.java",
	), "java", nil)
	if even.Detected() {
		t.Errorf("even split should not yield a convention; got %q", even.Suffix)
	}
	if !even.Ambiguous {
		t.Error("an even split must report Ambiguous so an operator can see the decision")
	}
}

// E2E artifacts have their own suffix chosen by the e2e suggester; counting them would drag the
// unit default toward a layer it does not apply to.
func TestDetectTestSuffixConvention_ignoresE2EArtifacts(t *testing.T) {
	conv := DetectTestSuffixConvention(fileVersions(
		petclinicPkg+"ATests.java", petclinicPkg+"BTests.java", petclinicPkg+"CTests.java",
		petclinicPkg+"XE2EIT.java", petclinicPkg+"YE2EIT.java", petclinicPkg+"ZE2EIT.java",
		petclinicPkg+"WE2EIT.java", petclinicPkg+"VE2EIT.java",
	), "java", nil)
	if conv.Suffix != "Tests" || conv.Samples != 3 {
		t.Errorf("e2e artifacts leaked into the unit vote: %s", conv.Describe())
	}
}

// The half that actually prevents the duplicate: a SUT with NO existing test must still get the
// repository's convention, or the next run finds two files and has to choose between them.
func TestExistingOrSuggestedTestPath_newArtifactFollowsRepoConvention(t *testing.T) {
	item := &retrieval.TestPlanItem{
		Layer: "unit",
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Lang: "java", Kind: "method",
			File:   "src/main/java/org/springframework/samples/petclinic/owner/Owner.java",
			FQName: "org.springframework.samples.petclinic.owner.Owner",
		}},
		Context: &retrieval.RetrievalContext{TestSuffixConvention: "Tests"},
	}
	got, hit, _ := ExistingOrSuggestedTestPath(item, "junit", "playwright-java", "", false)
	want := "src/test/java/org/springframework/samples/petclinic/owner/OwnerTests.java"
	if filepath.ToSlash(got) != want {
		t.Errorf("new artifact path = %s, want %s", got, want)
	}
	if hit {
		t.Error("existingHit must be false when there are no candidates")
	}
}

// Extending beats duplicating: an off-convention file that already exists still wins over a
// convention-perfect path that does not.
func TestExistingOrSuggestedTestPath_existingOffConventionFileStillWins(t *testing.T) {
	repo := t.TempDir()
	rel := petclinicPkg + "OwnerControllerTest.java"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package p;\nclass OwnerControllerTest {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &retrieval.TestPlanItem{
		Layer: "unit",
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Lang: "java", Kind: "method",
			File:   "src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java",
			FQName: "org.springframework.samples.petclinic.owner.OwnerController",
		}},
		Context: &retrieval.RetrievalContext{
			TestSuffixConvention: "Tests",
			ExistingTestPaths:    []string{rel},
		},
	}
	got, hit, _ := ExistingOrSuggestedTestPath(item, "junit", "playwright-java", repo, false)
	if filepath.ToSlash(got) != rel || !hit {
		t.Errorf("got (%s, hit=%v); want the existing off-convention file %s extended", got, hit, rel)
	}
}

// The chain has to hold end to end: a repository whose suite is *Tests must get a *Tests default
// for a brand-new artifact, or every run writes a sibling beside the file it should have extended
// — and once both exist the redirect picks on sort order ("Test.java" precedes "Tests.java"), so
// the tool's own leftover shadows the real suite permanently.
func TestConventionReachesTheDefaultPath(t *testing.T) {
	item := &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Kind: "METHOD", Lang: "java", File: "src/main/java/p/Owner.java",
		}},
		Context: &retrieval.RetrievalContext{},
	}

	// Undetected: the built-in default stands.
	if got := SuggestedTestPath(item, "", "", ""); !strings.HasSuffix(got, "OwnerTest.java") {
		t.Fatalf("no convention should leave the default alone, got %q", got)
	}

	// Detected: the default follows the repository.
	item.Context.TestSuffixConvention = "Tests"
	got := SuggestedTestPath(item, "", "", "")
	if !strings.HasSuffix(got, "OwnerTests.java") {
		t.Fatalf("default path = %q, want the repo's *Tests convention", got)
	}
}

// The vote must exclude ASQS-authored files. Upstream measured a fixture repo reading 14 *Tests to
// 1 *Test (93%) before a run, and 14 to 9 (61%) after one committed seven *Test files — one more
// run and the tool would have voted its own mistake into the house style.
func TestDetectTestSuffixConvention_excludesGeneratedFiles(t *testing.T) {
	var files []indexer.FileVersion
	for _, p := range []string{
		"src/test/java/p/ATests.java", "src/test/java/p/BTests.java", "src/test/java/p/CTests.java",
		"src/test/java/p/DTests.java", "src/test/java/p/ETests.java",
	} {
		files = append(files, indexer.FileVersion{Path: p})
	}
	// Five ASQS-written *Test.java files that would otherwise swamp the vote.
	generated := genmanifest.Set{}
	for _, p := range []string{
		"src/test/java/p/VTest.java", "src/test/java/p/WTest.java", "src/test/java/p/XTest.java",
		"src/test/java/p/YTest.java", "src/test/java/p/ZTest.java",
	} {
		files = append(files, indexer.FileVersion{Path: p})
		generated[genmanifest.Normalize(p)] = genmanifest.Entry{}
	}

	conv := DetectTestSuffixConvention(files, "java", generated)
	if conv.Suffix != "Tests" {
		t.Fatalf("suffix = %q (samples=%d share=%.2f), want Tests — the tool voted its own files into the house style",
			conv.Suffix, conv.Samples, conv.Share)
	}
	if conv.ExcludedGenerated != 5 {
		t.Errorf("ExcludedGenerated = %d, want 5", conv.ExcludedGenerated)
	}
}
