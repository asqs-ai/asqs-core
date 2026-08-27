package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/generator/extendmerge"
	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// Detection and reconciliation of duplicate test artifacts already on disk.
//
// F08 stops new duplicates being created. It does nothing for the repositories that already carry
// them — including the fixture repo, which holds both OwnerControllerTest.java and
// OwnerControllerTests.java, and both VetControllerTest.java and VetControllerTests.java. Both
// members of each pair match Surefire's default includes, so both run.
//
// Leaving them costs more after F01 than before it. F01 gives the fixer write access to any failing
// test file the diagnostic names, so a duplicate pair means the fixer can spend rounds repairing a
// file that should not exist. That is exactly what happened: three of five rounds went into
// OwnerControllerTest.java, the leftover, while the canonical OwnerControllerTests.java sat
// untouched and compiling.
//
// Two rules make this safe enough to ship:
//
//   - Only a file recorded as ASQS-authored in the provenance manifest may be deleted. Absent
//     provenance the pair is reported and skipped. Deleting a developer's test because it lost a
//     naming vote is the one outcome worse than the duplicate.
//   - Reconciliation runs BEFORE planning or not at all. A path that moves after
//     ResolveGenerateTestPath has reserved it invalidates the plan's reservations and the fixer's
//     FixRequest.ArtifactPaths.

// DuplicateGroup is a set of test files that all back-link to the same source file and differ only
// by naming convention.
type DuplicateGroup struct {
	// SourcePath is the SUT they all claim to test.
	SourcePath string
	// Canonical is the member that should survive, chosen by F08's ranking.
	Canonical string
	// Redundant are the other members, sorted.
	Redundant []string
	// GeneratedByASQS lists the members recorded in the provenance manifest.
	GeneratedByASQS []string
}

// Reconcilable reports whether every redundant member is ASQS-authored, which is the precondition
// for deleting any of them.
func (g DuplicateGroup) Reconcilable() bool {
	if len(g.Redundant) == 0 {
		return false
	}
	gen := make(map[string]bool, len(g.GeneratedByASQS))
	for _, p := range g.GeneratedByASQS {
		gen[p] = true
	}
	for _, p := range g.Redundant {
		if !gen[p] {
			return false
		}
	}
	return true
}

// conventionStem strips a known unit-test naming suffix, returning the bare SUT-ish stem and true
// when a suffix was present. `FooTests.java` and `FooTest.java` both reduce to `Foo`, which is what
// makes them recognisable as the same artifact under two conventions.
//
// `IT` is deliberately NOT stripped: FooIT.java runs under Failsafe in a different lifecycle phase
// from a Surefire FooTest.java, so they are distinct artifacts rather than duplicates.
func conventionStem(path string) (stem string, ok bool) {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	switch strings.ToLower(ext) {
	case ".java", ".cs":
		s := strings.TrimSuffix(base, ext)
		for _, suffix := range []string{"Tests", "Test"} {
			if strings.HasSuffix(s, suffix) && len(s) > len(suffix) {
				return strings.TrimSuffix(s, suffix), true
			}
		}
		return "", false
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		lb := strings.ToLower(base)
		for _, marker := range []string{".test.", ".spec."} {
			if i := strings.Index(lb, marker); i > 0 {
				return base[:i], true
			}
		}
		return "", false
	}
	return "", false
}

// FindDuplicateTestArtifacts groups indexed test files that target the same source file and differ
// only by naming convention.
//
// Layer is decisive and checked first: FooTests.java (unit) and FooE2EIT.java (e2e) legitimately
// coexist, and IsE2ETestPath already owns that judgement — re-deciding it here is how the two would
// drift apart.
func FindDuplicateTestArtifacts(files []indexer.FileVersion, lang, testFramework, repoPath string, generated genmanifest.Set) []DuplicateGroup {
	type member struct {
		path string
		dir  string
		stem string
	}
	bySource := map[string][]member{}
	for _, fv := range files {
		p := genmanifest.Normalize(fv.Path)
		if p == "" || IsE2ETestPath(p) {
			continue
		}
		stem, ok := conventionStem(p)
		if !ok {
			continue
		}
		src := retrieval.TestPathToSourcePath(p, lang, testFramework, repoPath)
		if strings.TrimSpace(src) == "" {
			continue
		}
		src = genmanifest.Normalize(src)
		bySource[src] = append(bySource[src], member{path: p, dir: filepath.ToSlash(filepath.Dir(p)), stem: stem})
	}

	var out []DuplicateGroup
	for src, members := range bySource {
		if len(members) < 2 {
			continue
		}
		// Same directory AND same stem: two files that differ only by the suffix convention. A
		// same-stem file in a different directory is a separate suite (e.g. an integration tree),
		// not a duplicate.
		byKey := map[string][]string{}
		for _, m := range members {
			byKey[m.dir+"\x00"+m.stem] = append(byKey[m.dir+"\x00"+m.stem], m.path)
		}
		for _, paths := range byKey {
			if len(paths) < 2 {
				continue
			}
			sort.Strings(paths)
			conv := DetectTestSuffixConvention(files, lang, generated)
			canonical := RankExistingTestPaths(paths, strings.ToLower(strings.TrimSpace(lang)), conv, generated, repoPath)
			g := DuplicateGroup{SourcePath: src, Canonical: canonical}
			for _, p := range paths {
				if p != canonical {
					g.Redundant = append(g.Redundant, p)
				}
				if generated.Has(p) {
					g.GeneratedByASQS = append(g.GeneratedByASQS, p)
				}
			}
			sort.Strings(g.Redundant)
			sort.Strings(g.GeneratedByASQS)
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Canonical < out[j].Canonical })
	return out
}

// ReconcileResult records what one reconciliation did.
type ReconcileResult struct {
	Group   DuplicateGroup
	Merged  []string // redundant members merged into the canonical file and deleted
	Skipped map[string]string
}

// ReconcileDuplicateTestArtifacts merges each redundant member into its canonical file and removes
// it. `backup` receives the original bytes of every path touched (nil for a deletion target's
// absence) so an aborted run can restore them exactly as ApplyDiscardPaths does for generated
// artifacts; pass nil to skip that.
//
// Returns one result per group, including groups it declined, so the caller can audit refusals as
// loudly as actions.
func ReconcileDuplicateTestArtifacts(repoPath string, groups []DuplicateGroup, backup func(path string, original []byte, existed bool)) []ReconcileResult {
	out := make([]ReconcileResult, 0, len(groups))
	for _, g := range groups {
		res := ReconcileResult{Group: g, Skipped: map[string]string{}}
		if !g.Reconcilable() {
			for _, p := range g.Redundant {
				res.Skipped[p] = "not recorded as ASQS-authored; refusing to delete a file this tool may not have written"
			}
			out = append(out, res)
			continue
		}
		canonicalFull := filepath.Join(repoPath, filepath.FromSlash(g.Canonical))
		canonicalBytes, err := os.ReadFile(canonicalFull)
		if err != nil {
			for _, p := range g.Redundant {
				res.Skipped[p] = fmt.Sprintf("cannot read canonical file %s: %v", g.Canonical, err)
			}
			out = append(out, res)
			continue
		}
		originalCanonical := append([]byte(nil), canonicalBytes...)
		merged := string(canonicalBytes)
		var mergedAny bool

		for _, redundant := range g.Redundant {
			redundantFull := filepath.Join(repoPath, filepath.FromSlash(redundant))
			body, readErr := os.ReadFile(redundantFull)
			if readErr != nil {
				res.Skipped[redundant] = fmt.Sprintf("cannot read: %v", readErr)
				continue
			}
			next, ok, why := mergeArtifactInto(merged, string(body), g.Canonical, redundant)
			if !ok {
				res.Skipped[redundant] = why
				continue
			}
			merged = next
			mergedAny = true
			if backup != nil {
				backup(redundant, append([]byte(nil), body...), true)
			}
			if err := os.Remove(redundantFull); err != nil {
				res.Skipped[redundant] = fmt.Sprintf("merged but could not remove: %v", err)
				continue
			}
			res.Merged = append(res.Merged, redundant)
		}
		if mergedAny {
			if backup != nil {
				backup(g.Canonical, originalCanonical, true)
			}
			if err := os.WriteFile(canonicalFull, []byte(merged), 0o644); err != nil {
				res.Skipped[g.Canonical] = fmt.Sprintf("cannot write merged canonical file: %v", err)
			}
		}
		out = append(out, res)
	}
	return out
}

// mergeArtifactInto folds the redundant file's members into the canonical one, sharing the exact
// merge the extend-existing write path uses. Sharing it is the point: a reconciler that spliced
// differently would produce a file generation could not then extend.
func mergeArtifactInto(canonical, redundant, canonicalPath, redundantPath string) (string, bool, string) {
	return extendmerge.MergeArtifact(canonical, redundant, canonicalPath, redundantPath)
}

// DescribeDuplicateGroup renders a group for an audit payload.
func DescribeDuplicateGroup(g DuplicateGroup) map[string]interface{} {
	return map[string]interface{}{
		"source_path":       g.SourcePath,
		"canonical":         g.Canonical,
		"redundant":         g.Redundant,
		"generated_by_asqs": g.GeneratedByASQS,
		"reconcilable":      g.Reconcilable(),
	}
}
