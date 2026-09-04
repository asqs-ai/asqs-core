package uitesthooks

import (
	"os"
	"path/filepath"
	"testing"
)

// The journal is the rollback for callers without a seam journal: the first snapshot wins, and
// RestoreAll puts the original bytes back.
func TestJournal_snapshotsOnceAndRestores(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "a.tsx")
	if err := os.WriteFile(full, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := NewJournal()
	if !j.Empty() {
		t.Fatal("new journal must be empty")
	}
	j.Snapshot(full, "a.tsx")
	if err := os.WriteFile(full, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	j.Snapshot(full, "a.tsx") // a second snapshot must not overwrite the original bytes
	if got := j.Rels(); len(got) != 1 || got[0] != "a.tsx" {
		t.Fatalf("rels = %v", got)
	}
	if restored := j.RestoreAll(); len(restored) != 1 {
		t.Fatalf("restored = %v", restored)
	}
	b, _ := os.ReadFile(full)
	if string(b) != "original" {
		t.Fatalf("file after restore = %q", b)
	}
	var nilJournal *Journal
	nilJournal.Snapshot(full, "a.tsx") // nil-safe
	if nilJournal.RestoreAll() != nil || !nilJournal.Empty() {
		t.Fatal("nil journal must be a no-op")
	}
}
