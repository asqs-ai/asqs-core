package jstindexer

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDist(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, "dist", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The fingerprint must follow the BUILT indexer: same dist, same value; any dist/*.js change, a
// different value; no dist at all, empty (so callers can skip stamping instead of stamping "").
func TestFingerprint_followsTheBuiltDist(t *testing.T) {
	a := t.TempDir()
	writeDist(t, a, map[string]string{"index.js": "run();", "enrichers.js": "export const x = 1;"})
	fpA := Fingerprint(a)
	if fpA == "" || len(fpA) != 16 {
		t.Fatalf("fingerprint = %q, want 16 hex chars", fpA)
	}
	if again := Fingerprint(a); again != fpA {
		t.Fatalf("fingerprint not stable: %q vs %q", fpA, again)
	}

	b := t.TempDir()
	writeDist(t, b, map[string]string{"index.js": "run();", "enrichers.js": "export const x = 1;"})
	if fpB := Fingerprint(b); fpB != fpA {
		t.Fatalf("identical dist in another directory must fingerprint the same: %q vs %q", fpA, fpB)
	}

	writeDist(t, b, map[string]string{"enrichers-jsx-hooks.js": "export function enrichFileJsxHooks() {}"})
	if fpB := Fingerprint(b); fpB == fpA {
		t.Fatal("a new dist file must change the fingerprint")
	}

	// .d.ts and other non-runtime files do not execute and must not matter.
	writeDist(t, a, map[string]string{"index.d.ts": "export {};"})
	if fpA2 := Fingerprint(a); fpA2 != fpA {
		t.Fatalf("a .d.ts file changed the fingerprint: %q vs %q", fpA, fpA2)
	}

	if fp := Fingerprint(t.TempDir()); fp != "" {
		t.Fatalf("no dist directory must yield an empty fingerprint, got %q", fp)
	}
	if fp := Fingerprint(""); fp != "" {
		t.Fatalf("empty path must yield an empty fingerprint, got %q", fp)
	}
}

// The configured path is the ENTRY FILE (indexer.jsts.indexer_path documents
// tools/js-ts-indexer/dist/index.js); the package root and the dist directory must fingerprint
// identically to it.
func TestFingerprint_acceptsEntryFileDistDirAndPackageRoot(t *testing.T) {
	root := t.TempDir()
	writeDist(t, root, map[string]string{"index.js": "run();", "enrichers-jsx-hooks.js": "x"})
	fromRoot := Fingerprint(root)
	fromDist := Fingerprint(filepath.Join(root, "dist"))
	fromEntry := Fingerprint(filepath.Join(root, "dist", "index.js"))
	if fromRoot == "" || fromRoot != fromDist || fromRoot != fromEntry {
		t.Fatalf("spellings disagree: root=%q dist=%q entry=%q", fromRoot, fromDist, fromEntry)
	}
	if fp := Fingerprint(filepath.Join(root, "dist", "missing.js")); fp != "" {
		t.Fatalf("a non-existent entry file must not fingerprint, got %q", fp)
	}
}
