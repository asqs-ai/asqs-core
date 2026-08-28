package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

const ownerPkg = "src/test/java/org/springframework/samples/petclinic/owner/"

func fvs(paths ...string) []indexer.FileVersion {
	out := make([]indexer.FileVersion, 0, len(paths))
	for _, p := range paths {
		out = append(out, indexer.FileVersion{Path: p})
	}
	return out
}

func genSet(paths ...string) genmanifest.Set {
	var entries []genmanifest.Entry
	for _, p := range paths {
		entries = append(entries, genmanifest.Entry{Path: p})
	}
	return genmanifest.Manifest{Entries: entries}.AsSet()
}

// The fixture repo's real state: an ASQS-authored OwnerControllerTest.java sitting beside the
// repository's own OwnerControllerTests.java, both matching Surefire's default includes.
func TestFindDuplicateTestArtifacts_findsTheFixtureRepoPair(t *testing.T) {
	leftover := ownerPkg + "OwnerControllerTest.java"
	canonical := ownerPkg + "OwnerControllerTests.java"
	files := fvs(
		leftover, canonical,
		ownerPkg+"PetValidatorTests.java", // unpaired, must not be flagged
		ownerPkg+"VisitControllerTests.java",
	)
	groups := FindDuplicateTestArtifacts(files, "java", "junit", "", genSet(leftover))

	if len(groups) != 1 {
		t.Fatalf("want exactly 1 duplicate group, got %d: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Canonical != canonical {
		t.Errorf("canonical = %s, want the repo's own %s", g.Canonical, canonical)
	}
	if len(g.Redundant) != 1 || g.Redundant[0] != leftover {
		t.Errorf("redundant = %v, want [%s]", g.Redundant, leftover)
	}
	if !g.Reconcilable() {
		t.Error("an ASQS-authored redundant member must be reconcilable")
	}
}

// A unit suite and an e2e suite for the same controller are different artifacts, not duplicates.
func TestFindDuplicateTestArtifacts_ignoresCrossLayerPairs(t *testing.T) {
	files := fvs(ownerPkg+"OwnerControllerTests.java", ownerPkg+"OwnerControllerE2EIT.java")
	if groups := FindDuplicateTestArtifacts(files, "java", "junit", "", nil); len(groups) != 0 {
		t.Errorf("unit + e2e must not be reported as duplicates: %+v", groups)
	}
}

// Without provenance we do not know who wrote either file, so nothing may be deleted.
func TestFindDuplicateTestArtifacts_unknownProvenanceIsNotReconcilable(t *testing.T) {
	files := fvs(ownerPkg+"OwnerControllerTest.java", ownerPkg+"OwnerControllerTests.java")
	groups := FindDuplicateTestArtifacts(files, "java", "junit", "", nil)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if groups[0].Reconcilable() {
		t.Error("a pair with no provenance must never be reconcilable")
	}
}

func writeFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileDuplicateTestArtifacts_mergesAndDeletes(t *testing.T) {
	repo := t.TempDir()
	canonical := ownerPkg + "OwnerControllerTests.java"
	leftover := ownerPkg + "OwnerControllerTest.java"

	// The SUT must exist: TestPathToSourcePath prefers a candidate whose mapped src/main path is
	// on disk, which is what makes both test files resolve to the SAME source.
	writeFile(t, repo, "src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java",
		"package org.springframework.samples.petclinic.owner;\n\nclass OwnerController {}\n")
	writeFile(t, repo, canonical, `package org.springframework.samples.petclinic.owner;

import org.junit.jupiter.api.Test;

class OwnerControllerTests {

	@Test
	void canonicalOne() {
	}

}
`)
	// The leftover brings a test AND an import the canonical file does not have. Without F04's
	// import union the merged file would not compile — which is the failure this whole plan exists
	// to stop, so reconciliation must not reintroduce it.
	writeFile(t, repo, leftover, `package org.springframework.samples.petclinic.owner;

import java.util.List;
import org.junit.jupiter.api.Test;

class OwnerControllerTest {

	@Test
	void leftoverOne() {
		List<String> xs = List.of("a");
	}

}
`)

	groups := FindDuplicateTestArtifacts(
		fvs(canonical, leftover), "java", "junit", repo, genSet(leftover))
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}

	var backedUp []string
	results := ReconcileDuplicateTestArtifacts(repo, groups, func(p string, _ []byte, _ bool) {
		backedUp = append(backedUp, p)
	})
	if len(results) != 1 || len(results[0].Merged) != 1 {
		t.Fatalf("merge did not happen: %+v", results)
	}

	mergedBytes, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(canonical)))
	if err != nil {
		t.Fatal(err)
	}
	merged := string(mergedBytes)
	for _, want := range []string{"canonicalOne", "leftoverOne", "import java.util.List;"} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged canonical file is missing %q:\n%s", want, merged)
		}
	}
	if strings.Count(merged, "class OwnerControllerTest") != 1 {
		t.Errorf("class header spliced into the body:\n%s", merged)
	}
	if strings.Count(merged, "package org.springframework") != 1 {
		t.Errorf("package line duplicated:\n%s", merged)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(leftover))); !os.IsNotExist(err) {
		t.Error("redundant file still on disk after reconciliation")
	}
	// Both touched paths must be recoverable if the run aborts.
	for _, want := range []string{canonical, leftover} {
		if !containsString(backedUp, want) {
			t.Errorf("%s was changed without a backup; an aborted run could not restore it", want)
		}
	}
}

// The provenance gate is the one that stops a developer's test being deleted for losing a naming
// vote. It must hold even when everything else about the group looks reconcilable.
func TestReconcileDuplicateTestArtifacts_refusesWithoutProvenance(t *testing.T) {
	repo := t.TempDir()
	canonical := ownerPkg + "OwnerControllerTests.java"
	handWritten := ownerPkg + "OwnerControllerTest.java"
	body := "package p;\n\nimport org.junit.jupiter.api.Test;\n\nclass C {\n\n\t@Test\n\tvoid a() {\n\t}\n\n}\n"
	writeFile(t, repo, "src/main/java/org/springframework/samples/petclinic/owner/OwnerController.java",
		"package org.springframework.samples.petclinic.owner;\n\nclass OwnerController {}\n")
	writeFile(t, repo, canonical, body)
	writeFile(t, repo, handWritten, body)

	groups := FindDuplicateTestArtifacts(fvs(canonical, handWritten), "java", "junit", repo, nil)
	results := ReconcileDuplicateTestArtifacts(repo, groups, nil)

	if len(results) != 1 || len(results[0].Merged) != 0 {
		t.Fatalf("a pair with no provenance must not be merged: %+v", results)
	}
	// Which member the ranking calls canonical is incidental here — with no convention (2 samples
	// is below the threshold) and no provenance it tie-breaks lexicographically. The invariant that
	// matters is that BOTH files survive and the refusal names the provenance gate.
	for _, p := range []string{canonical, handWritten} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s was deleted despite having no provenance", p)
		}
	}
	if len(results[0].Skipped) == 0 {
		t.Fatal("refusal must be recorded, not silent")
	}
	for p, reason := range results[0].Skipped {
		if !strings.Contains(reason, "ASQS-authored") {
			t.Errorf("skip reason for %s should name the provenance gate; got %q", p, reason)
		}
	}
}

func TestConventionStem(t *testing.T) {
	cases := []struct {
		path, stem string
		ok         bool
	}{
		{"a/FooTests.java", "Foo", true},
		{"a/FooTest.java", "Foo", true},
		{"a/FooIT.java", "", false}, // Failsafe lifecycle: a distinct artifact
		{"a/Test.java", "", false},  // bare "Test" has no stem left
		{"a/Foo.java", "", false},   // not a test file
		{"a/foo.test.ts", "foo", true},
		{"a/foo.spec.ts", "foo", true},
		{"a/foo.ts", "", false},
		{"a/FooTests.cs", "Foo", true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			stem, ok := conventionStem(tc.path)
			if ok != tc.ok || stem != tc.stem {
				t.Errorf("got (%q,%v), want (%q,%v)", stem, ok, tc.stem, tc.ok)
			}
		})
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// F11's reconciliation shares this machinery, so the C# fixes reach it without separate work.
func TestReconcileCSharp_mergesUsingStatic(t *testing.T) {
	repo := t.TempDir()
	canonical := "tests/Petclinic.Tests/OwnerControllerTests.cs"
	leftover := "tests/Petclinic.Tests/OwnerControllerTest.cs"
	writeFile(t, repo, "src/Petclinic/Owners/OwnerController.cs", "namespace N;\npublic class OwnerController { }\n")
	writeFile(t, repo, canonical, xunitOwnerTests)
	writeFile(t, repo, leftover, `using static FluentAssertions.AssertionExtensions;

namespace Petclinic.Owners.Tests;

public class OwnerControllerTest
{
    [Fact]
    public void Leftover()
    {
    }
}
`)
	groups := FindDuplicateTestArtifacts(
		fvs(canonical, leftover), "csharp", "xunit", repo, genSet(leftover))
	if len(groups) != 1 {
		t.Fatalf("want 1 duplicate group, got %d: %+v", len(groups), groups)
	}
	results := ReconcileDuplicateTestArtifacts(repo, groups, nil)
	if len(results) != 1 || len(results[0].Merged) != 1 {
		t.Fatalf("merge did not happen: %+v", results)
	}
	merged, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(canonical)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(merged)
	classAt := strings.Index(got, "public class OwnerControllerTests")
	at := strings.Index(got, "using static FluentAssertions.AssertionExtensions;")
	if at < 0 || at > classAt {
		t.Errorf("reconciliation put a using clause inside the class body:\n%s", got)
	}
	if !strings.Contains(got, "Leftover") {
		t.Errorf("merged file lost the redundant member's test:\n%s", got)
	}
}

const xunitOwnerTests = `// Copyright (c) The ASQS Authors.

using System;
using static Xunit.Assert;

namespace Petclinic.Owners.Tests;

public class OwnerControllerTests
{
    [Fact]
    public void Existing()
    {
        True(true);
    }
}
`
