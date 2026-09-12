package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// `dotnet test --collect "XPlat Code Coverage"` writes
// <project>/TestResults/<random guid>/coverage.cobertura.xml — under the TEST PROJECT, not the eval
// cwd, and one directory deeper than a single-level glob can see. The configured pattern
// "TestResults/*/coverage.cobertura.xml" resolved with filepath.Glob from the eval cwd, so it found
// the report only in the one layout where the eval cwd happened to be the test project itself.
func TestFindCoverageReport_walksNestedTestResults(t *testing.T) {
	repo := t.TempDir()
	report := filepath.Join(repo, "tests", "App.Tests", "TestResults", "6f1b2c3d-aaaa", "coverage.cobertura.xml")
	mustWriteReport(t, report, "<coverage/>")

	got := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"})
	want := "tests/App.Tests/TestResults/6f1b2c3d-aaaa/coverage.cobertura.xml"
	if got != want {
		t.Fatalf("findCoverageReport = %q, want %q", got, want)
	}
}

// An exact, non-glob path still resolves directly and is not sent through the walk.
func TestFindCoverageReport_exactPathStillWorks(t *testing.T) {
	repo := t.TempDir()
	mustWriteReport(t, filepath.Join(repo, "target", "site", "jacoco", "index.html"), "<html/>")

	if got := findCoverageReport(repo, []string{"target/site/jacoco/index.html"}); got != "target/site/jacoco/index.html" {
		t.Fatalf("findCoverageReport = %q, want the exact path", got)
	}
}

// Build output must not be mistaken for a report, and neither must a directory of that name.
func TestFindCoverageReport_skipsBuildOutputAndDirectories(t *testing.T) {
	repo := t.TempDir()
	mustWriteReport(t, filepath.Join(repo, "src", "App", "bin", "Release", "TestResults", "x", "coverage.cobertura.xml"), "<coverage/>")
	if got := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"}); got != "" {
		t.Fatalf("findCoverageReport = %q, want none: the only match is under bin/", got)
	}

	repo2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo2, "TestResults", "x", "coverage.cobertura.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findCoverageReport(repo2, []string{"TestResults/*/coverage.cobertura.xml"}); got != "" {
		t.Fatalf("findCoverageReport = %q, want none: the match is a directory", got)
	}
}

// Deterministic choice when several projects each produced a report: the first in sorted order.
func TestFindCoverageReport_deterministicAcrossProjects(t *testing.T) {
	repo := t.TempDir()
	mustWriteReport(t, filepath.Join(repo, "tests", "B.Tests", "TestResults", "g2", "coverage.cobertura.xml"), "<coverage/>")
	mustWriteReport(t, filepath.Join(repo, "tests", "A.Tests", "TestResults", "g1", "coverage.cobertura.xml"), "<coverage/>")

	first := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"})
	if first != "tests/A.Tests/TestResults/g1/coverage.cobertura.xml" {
		t.Fatalf("findCoverageReport = %q, want the lexicographically first match", first)
	}
	if again := findCoverageReport(repo, []string{"TestResults/*/coverage.cobertura.xml"}); again != first {
		t.Fatalf("second call returned %q, want the same %q", again, first)
	}
}

func mustWriteReport(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
