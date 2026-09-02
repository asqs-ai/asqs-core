package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectJestBootstrapIsTS_lang(t *testing.T) {
	dir := t.TempDir()
	if !detectJestBootstrapIsTS(dir, "typescript") {
		t.Fatal("want true for typescript lang")
	}
	if detectJestBootstrapIsTS(dir, "javascript") {
		t.Fatal("want false for plain javascript without ts artifacts")
	}
}

func TestDetectJestBootstrapIsTS_tsconfigGlob(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.app.json"), []byte(`{"compilerOptions":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if !detectJestBootstrapIsTS(dir, "javascript") {
		t.Fatal("want true when tsconfig.app.json exists")
	}
}

func TestDetectJestBootstrapIsTS_packageJSON(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","devDependencies":{"typescript":"^5.0.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if !detectJestBootstrapIsTS(dir, "javascript") {
		t.Fatal("want true when typescript is a dependency")
	}
}

func TestStripTrailingJSONCommas(t *testing.T) {
	in := []byte(`{"a":1,}`)
	out := stripTrailingJSONCommas(in)
	if string(out) != `{"a":1}` {
		t.Fatalf("got %q", out)
	}
}

func TestPatchRootTSConfigForJest_types(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tsconfig.json")
	raw := `{
  "compilerOptions": {
    "types": ["node"]
  }
}`
	if err := os.WriteFile(p, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := patchRootTSConfigForTestGlobals(p, jestTSGlobals)
	if err != nil || !ok {
		t.Fatalf("patch: ok=%v err=%v", ok, err)
	}
	out, _ := os.ReadFile(p)
	if !containsBytes(out, `"jest"`) {
		t.Fatalf("expected jest in types: %s", out)
	}
	ok2, err := patchRootTSConfigForTestGlobals(p, jestTSGlobals)
	if err != nil || ok2 {
		t.Fatalf("second patch should be no-op: ok=%v err=%v", ok2, err)
	}
}

func TestPatchRootTSConfigForJest_include(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tsconfig.json")
	raw := `{"compilerOptions":{"strict":true},"include":["src"]}`
	if err := os.WriteFile(p, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := patchRootTSConfigForTestGlobals(p, jestTSGlobals)
	if err != nil || !ok {
		t.Fatalf("patch: ok=%v err=%v", ok, err)
	}
	out, _ := os.ReadFile(p)
	if !containsBytes(out, jestTSGlobals.DTSFile) {
		t.Fatalf("expected globals file in include: %s", out)
	}
}

func TestPatchRootTSConfigForJest_comments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tsconfig.json")
	raw := `{
  // top
  "compilerOptions": {
    "types": ["node"],
  },
}`
	if err := os.WriteFile(p, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := patchRootTSConfigForTestGlobals(p, jestTSGlobals)
	if err != nil || !ok {
		t.Fatalf("patch: ok=%v err=%v", ok, err)
	}
	out, _ := os.ReadFile(p)
	if !strings.Contains(string(out), `"jest"`) {
		t.Fatalf("expected jest after comment strip: %s", out)
	}
}

func containsBytes(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}
