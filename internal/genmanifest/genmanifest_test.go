package genmanifest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRecordAndLoad(t *testing.T) {
	repo := t.TempDir()

	if s := LoadSet(repo); s.Known() {
		t.Error("a repo ASQS never wrote to must report no provenance")
	}

	n, err := Record(repo, "run-1", []string{"src/test/java/p/ATest.java", "/src/test/java/p/BTest.java"})
	if err != nil || n != 2 {
		t.Fatalf("Record = (%d, %v), want (2, nil)", n, err)
	}
	s := LoadSet(repo)
	if !s.Has("src/test/java/p/ATest.java") || !s.Has("src/test/java/p/BTest.java") {
		t.Fatalf("recorded paths missing from set: %v", s)
	}
	if s.Has("src/test/java/p/CTest.java") {
		t.Error("unrecorded path reported as ASQS-authored")
	}

	// Authorship is first-write, not last-touch: a later run extending the same file must not
	// take credit for it.
	if n, err := Record(repo, "run-2", []string{"src/test/java/p/ATest.java"}); err != nil || n != 0 {
		t.Fatalf("re-recording an existing path = (%d, %v), want (0, nil)", n, err)
	}
	for _, e := range Load(repo).Entries {
		if e.Path == "src/test/java/p/ATest.java" && e.RunID != "run-1" {
			t.Errorf("run id changed on re-record: %q", e.RunID)
		}
	}
}

// A corrupt manifest must degrade to "no provenance", never fail a run.
func TestLoadToleratesGarbage(t *testing.T) {
	repo := t.TempDir()
	full := filepath.Join(repo, filepath.FromSlash(RelPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := LoadSet(repo); s.Known() {
		t.Error("garbage manifest must yield an empty set")
	}
}

// Generation writes tests concurrently (gap_concurrency defaults to 8) and every gap may append.
func TestRecordIsConcurrencySafe(t *testing.T) {
	repo := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = Record(repo, "run-1", []string{filepath.ToSlash(filepath.Join("src/test/java/p", string(rune('A'+i))+"Test.java"))})
		}(i)
	}
	wg.Wait()
	if got := len(Load(repo).Entries); got != 8 {
		t.Errorf("concurrent Record lost entries: got %d, want 8", got)
	}
}
