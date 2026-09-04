package indexer

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// detectListerStub answers ListFiles from a fixed list; DetectChanges calls nothing else on the
// writer. The embedded nil interface makes any other call an explicit panic rather than a silent
// success.
type detectListerStub struct {
	MetadataWriter
	files []*metadata.File
}

func (m *detectListerStub) ListFiles(_ context.Context, repoID, _ string, _ *bool) ([]*metadata.File, error) {
	var out []*metadata.File
	for _, f := range m.files {
		if f != nil && f.RepoID == repoID {
			out = append(out, f)
		}
	}
	return out, nil
}

// The stamp reaches only the files the JS/TS indexer parses, replaces an older stamp rather than
// stacking one, and is a no-op for an empty fingerprint.
func TestStampLangIndexerFingerprint(t *testing.T) {
	files := []FileVersion{
		{Path: "src/a.tsx", SHA: "aaa", Lang: "typescript"},
		{Path: "src/b.js", SHA: "bbb", Lang: "javascript"},
		{Path: "public/index.html", SHA: "hhh", Lang: "html"},
		{Path: "src/Main.java", SHA: "jjj", Lang: "java"},
		{Path: "src/c.ts", SHA: "ccc+old0000000000", Lang: "typescript"},
	}
	if n := StampLangIndexerFingerprint(files, ""); n != 0 || files[0].SHA != "aaa" {
		t.Fatalf("empty fingerprint must stamp nothing; n=%d sha=%q", n, files[0].SHA)
	}
	n := StampLangIndexerFingerprint(files, "deadbeefcafe0001")
	if n != 4 {
		t.Fatalf("stamped %d, want 4 (java untouched)", n)
	}
	for i, want := range []string{"aaa+deadbeefcafe0001", "bbb+deadbeefcafe0001", "hhh+deadbeefcafe0001", "jjj", "ccc+deadbeefcafe0001"} {
		if files[i].SHA != want {
			t.Errorf("%s: SHA = %q, want %q", files[i].Path, files[i].SHA, want)
		}
	}
}

// THE FAILURE THIS CLOSES: an unchanged repository indexed by a rebuilt indexer reported
// "0 added, 0 changed, 0 removed" and the new enricher never ran. With the build fingerprint in
// the file version, the same content under a new build is a changed file; under the same build
// it stays unchanged.
func TestDetectChanges_rebuiltIndexerReindexesUnchangedFiles(t *testing.T) {
	stored := &detectListerStub{files: []*metadata.File{
		{File: "src/a.tsx", SHA: "aaa+build0000000001", Lang: "typescript", RepoID: "r"},
		{File: "src/Main.java", SHA: "jjj", Lang: "java", RepoID: "r"},
	}}
	current := []FileVersion{
		{Path: "src/a.tsx", SHA: "aaa", Lang: "typescript"},
		{Path: "src/Main.java", SHA: "jjj", Lang: "java"},
	}

	same := append([]FileVersion(nil), current...)
	StampLangIndexerFingerprint(same, "build0000000001")
	cs, err := DetectChanges(context.Background(), same, stored, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Added)+len(cs.Changed)+len(cs.Removed) != 0 {
		t.Fatalf("same build, same content must be a no-op; got added=%v changed=%v removed=%v", cs.Added, cs.Changed, cs.Removed)
	}

	rebuilt := append([]FileVersion(nil), current...)
	StampLangIndexerFingerprint(rebuilt, "build0000000002")
	cs, err = DetectChanges(context.Background(), rebuilt, stored, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Changed) != 1 || cs.Changed[0].Path != "src/a.tsx" {
		t.Fatalf("a rebuilt indexer must re-index the JS/TS file; changed=%v", cs.Changed)
	}
	if len(cs.Added) != 0 || len(cs.Removed) != 0 {
		t.Errorf("only the stamped file may change; added=%v removed=%v", cs.Added, cs.Removed)
	}
}
