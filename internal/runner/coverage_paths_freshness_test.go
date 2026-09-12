package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// `dotnet test --collect` writes a fresh TestResults/<random guid>/coverage.cobertura.xml on every
// invocation and never removes the previous one. Sorting GUIDs lexicographically picks an arbitrary
// run, so in a fix loop that runs coverage twice, or a workspace reused across runs, the summary
// could name a report from an earlier iteration.
func TestFindCoverageReport_prefersTheNewestReport(t *testing.T) {
	repo := t.TempDir()
	old := filepath.Join(repo, "tests", "App.Tests", "TestResults", "ffff-old", "coverage.cobertura.xml")
	recent := filepath.Join(repo, "tests", "App.Tests", "TestResults", "0000-new", "coverage.cobertura.xml")
	mustWriteReport(t, old, "<coverage/>")
	mustWriteReport(t, recent, "<coverage/>")
	// "0000-new" sorts first by name but is the newer file; make the ordering unambiguous.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	got := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"})
	if got != "tests/App.Tests/TestResults/0000-new/coverage.cobertura.xml" {
		t.Fatalf("findCoverageReport = %q, want the newest report", got)
	}

	// And with the names swapped, so the result cannot be an accident of sort order.
	repo2 := t.TempDir()
	oldB := filepath.Join(repo2, "TestResults", "0000-old", "coverage.cobertura.xml")
	newB := filepath.Join(repo2, "TestResults", "ffff-new", "coverage.cobertura.xml")
	mustWriteReport(t, oldB, "<coverage/>")
	mustWriteReport(t, newB, "<coverage/>")
	if err := os.Chtimes(oldB, past, past); err != nil {
		t.Fatal(err)
	}
	if got := findCoverageReport(repo2, []string{"TestResults/*/coverage.cobertura.xml"}); got != "TestResults/ffff-new/coverage.cobertura.xml" {
		t.Fatalf("findCoverageReport = %q, want the newest report", got)
	}
}

// Equal timestamps still resolve deterministically rather than by directory order.
func TestFindCoverageReport_tiesBreakByPath(t *testing.T) {
	repo := t.TempDir()
	a := filepath.Join(repo, "tests", "A.Tests", "TestResults", "g1", "coverage.cobertura.xml")
	b := filepath.Join(repo, "tests", "B.Tests", "TestResults", "g2", "coverage.cobertura.xml")
	mustWriteReport(t, a, "<coverage/>")
	mustWriteReport(t, b, "<coverage/>")
	stamp := time.Now().Add(-time.Minute)
	for _, p := range []string{a, b} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	first := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"})
	if first != "tests/A.Tests/TestResults/g1/coverage.cobertura.xml" {
		t.Fatalf("findCoverageReport = %q, want the lexicographically first of two equally fresh reports", first)
	}
	if again := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"}); again != first {
		t.Fatalf("second call returned %q, want the same %q", again, first)
	}
}
