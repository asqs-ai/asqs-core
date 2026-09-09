package testbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const nestBuildTSConfig = `{
  "extends": "./tsconfig.json",
  "exclude": ["node_modules", "dist", "**/*spec.ts"]
}
`

func readExclude(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Extends string   `json:"extends"`
		Exclude []string `json:"exclude"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("tsconfig.build.json is no longer valid JSON: %v\n%s", err, b)
	}
	if root.Extends != "./tsconfig.json" {
		t.Errorf("extends was lost: %q", root.Extends)
	}
	return root.Exclude
}

// Run api-7425be21b83f608e66318fdd99528522. tsc computes the emit root from the files it compiles;
// a Nest build tsconfig has no `include`, so the bootstrap's own files outside src/ (__tests__/,
// e2e/, playwright.config.ts) moved that root from src/ to the package root, `nest build` emitted
// dist/src/main.js, and the repository's `npm run start` (`node dist/main.js`) failed with "Cannot
// find module". That broke the E2E web server in the run and would break the repository's own
// start script in the shipped PR. Excluding what the bootstrap adds restores dist/main.js.
func TestEnsureBuildTSConfigExcludes_addsBootstrapPathsToNestBuild(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.build.json": nestBuildTSConfig})

	rel, changed, err := ensureBuildTSConfigExcludes(dir, []string{"__tests__", "**/*.test.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || rel != "tsconfig.build.json" {
		t.Fatalf("changed=%v rel=%q, want the build tsconfig patched", changed, rel)
	}
	got := readExclude(t, filepath.Join(dir, "tsconfig.build.json"))
	want := []string{"node_modules", "dist", "**/*spec.ts", "__tests__", "**/*.test.ts"}
	if len(got) != len(want) {
		t.Fatalf("exclude = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exclude[%d] = %q, want %q (existing entries first, in order)", i, got[i], want[i])
		}
	}
}

func TestEnsureBuildTSConfigExcludes_isIdempotent(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.build.json": nestBuildTSConfig})
	if _, _, err := ensureBuildTSConfigExcludes(dir, []string{"e2e", "playwright.config.ts"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "tsconfig.build.json"))

	_, changed, err := ensureBuildTSConfigExcludes(dir, []string{"e2e", "playwright.config.ts"})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "tsconfig.build.json"))
	if changed || string(before) != string(after) {
		t.Errorf("second call changed=%v and rewrote the file:\n%s", changed, after)
	}
	if got := readExclude(t, filepath.Join(dir, "tsconfig.build.json")); len(got) != 5 {
		t.Errorf("exclude = %v, want each pattern once", got)
	}
}

// Without a build tsconfig there is no separate build to protect; tsconfig.json is what ts-jest
// and the type-check gate read, and excluding tests from it would blind both.
func TestEnsureBuildTSConfigExcludes_leavesPackagesWithoutOneAlone(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.json": `{"compilerOptions":{"outDir":"./dist"}}`})

	rel, changed, err := ensureBuildTSConfigExcludes(dir, []string{"__tests__"})
	if err != nil || changed || rel != "" {
		t.Errorf("rel=%q changed=%v err=%v, want a no-op", rel, changed, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tsconfig.json")); string(b) != `{"compilerOptions":{"outDir":"./dist"}}` {
		t.Errorf("tsconfig.json was touched:\n%s", b)
	}
}

// An explicit include already scopes the build; files outside it never move the emit root.
func TestEnsureBuildTSConfigExcludes_respectsAnExplicitInclude(t *testing.T) {
	src := `{"extends": "./tsconfig.json", "include": ["src/**/*"], "exclude": ["node_modules"]}`
	dir := writePkg(t, map[string]string{"tsconfig.build.json": src})

	_, changed, err := ensureBuildTSConfigExcludes(dir, []string{"__tests__"})
	if err != nil || changed {
		t.Errorf("changed=%v err=%v, want untouched", changed, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tsconfig.build.json")); string(b) != src {
		t.Errorf("file was rewritten:\n%s", b)
	}
}

// A build tsconfig with comments (tsc accepts them) is patched rather than skipped.
func TestEnsureBuildTSConfigExcludes_toleratesComments(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.build.json": "{\n  // build only\n  \"extends\": \"./tsconfig.json\",\n  \"exclude\": [\"node_modules\", \"dist\",],\n}\n"})

	_, changed, err := ensureBuildTSConfigExcludes(dir, []string{"e2e"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want patched", changed, err)
	}
	if got := readExclude(t, filepath.Join(dir, "tsconfig.build.json")); len(got) != 3 || got[2] != "e2e" {
		t.Errorf("exclude = %v", got)
	}
}

// The patched file keeps the repository's key order: `extends` stays first, so the diff a reviewer
// sees is the added exclude entries and nothing else.
func TestEnsureBuildTSConfigExcludes_keepsKeyOrder(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.build.json": nestBuildTSConfig})
	if _, _, err := ensureBuildTSConfigExcludes(dir, playwrightBuildExcludes); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tsconfig.build.json"))
	s := string(b)
	if !(len(s) > 0 && s[0] == '{') || indexOf(s, `"extends"`) > indexOf(s, `"exclude"`) {
		t.Errorf("key order changed:\n%s", s)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The wiring used by both bootstraps: writes, audits, and reports the path for files_changed.
func TestExcludeBootstrapPathsFromBuild_reportsThePatchedFile(t *testing.T) {
	dir := writePkg(t, map[string]string{"tsconfig.build.json": nestBuildTSConfig})
	got := excludeBootstrapPathsFromBuild(t.Context(), nil, "e2e_bootstrap", dir, dir, playwrightBuildExcludes)
	if len(got) != 1 || got[0] != "tsconfig.build.json" {
		t.Errorf("files changed = %v, want the build tsconfig", got)
	}
	if again := excludeBootstrapPathsFromBuild(t.Context(), nil, "e2e_bootstrap", dir, dir, playwrightBuildExcludes); len(again) != 0 {
		t.Errorf("second call reported %v, want nothing", again)
	}
}
