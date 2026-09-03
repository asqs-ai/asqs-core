package errout

import (
	"os"
	"path/filepath"
	"testing"
)

// AllCitedRepoPaths is what the evaluator's writable-scope narrowing runs on. It resolved NOTHING
// from a TypeScript compile log until the parenthesised position shape was parsed, so a compile
// round shipped every artifact to the fixer whether or not the compiler had named it.
func TestAllCitedRepoPaths_tscAndMavenShapes(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{
		"src/app/AppLayout.test.tsx",
		"src/features/orders/orderFormat.test.ts",
		"src/main/java/com/example/Foo.java",
		"Controllers/OwnerController.cs",
	} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := "src/app/AppLayout.test.tsx(34,22): error TS2339: Property 'toBeInTheDocument' does not exist.\n" +
		"src/features/orders/orderFormat.test.ts(27,22): error TS2304: Cannot find name 'formatOrderRef'.\n" +
		"Controllers/OwnerController.cs(33,12): error CS1002: ; expected\n" +
		"src/main/java/com/example/Foo.java:[12,5] error: cannot find symbol\n"

	got := AllCitedRepoPaths(log, repo)
	seen := map[string]bool{}
	for _, p := range got {
		seen[p] = true
	}
	for _, want := range []string{
		"src/app/AppLayout.test.tsx",
		"src/features/orders/orderFormat.test.ts",
		"Controllers/OwnerController.cs",
		"src/main/java/com/example/Foo.java",
	} {
		if !seen[want] {
			t.Errorf("AllCitedRepoPaths did not resolve %s; got %v", want, got)
		}
	}
}

// A file the log never names must not be resolved — narrowing that returns everything is the bug
// this pattern exists to avoid.
func TestAllCitedRepoPaths_uncitedFileNotReturned(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{"src/a.test.tsx", "src/b.test.tsx"} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := AllCitedRepoPaths("src/a.test.tsx(1,1): error TS1005: ',' expected.\n", repo)
	for _, p := range got {
		if p == "src/b.test.tsx" {
			t.Fatalf("resolved an uncited file: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "src/a.test.tsx" {
		t.Errorf("got %v, want [src/a.test.tsx]", got)
	}
}

// AllCitedRepoPaths feeds the fixer's writable-scope narrowing. On coloured vitest output it
// resolved only the file an uncoloured React warning named (`at NavLink (/workspace/src/.../
// ExtrasLayout.test.tsx:13:21)`), so six failing files were dropped from the writable set while
// the one cited by a prop warning was kept.
func TestAllCitedRepoPaths_colouredVitestOutput(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{"src/app/router.test.tsx", "src/pages/extras/ExtrasLayout.test.tsx"} {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw := "    at NavLink (/workspace/src/pages/extras/ExtrasLayout.test.tsx:13:21)\n" +
		"\x1b[36m \x1b[2m❯\x1b[22m src/app/router.test.tsx:\x1b[2m59:24\x1b[22m\x1b[39m\n"
	got := AllCitedRepoPaths(raw, repo)
	seen := map[string]bool{}
	for _, p := range got {
		seen[p] = true
	}
	if !seen["src/app/router.test.tsx"] || !seen["src/pages/extras/ExtrasLayout.test.tsx"] {
		t.Fatalf("want both the coloured vitest frame and the plain warning frame; got %v", got)
	}
}
