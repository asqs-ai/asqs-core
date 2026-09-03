package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func supportRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"package.json": `{"name":"x"}`}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The helpers are what turn "assert on data the app can never receive" into a test that decides its
// own answer. Verified by running specs that import them against the unmodified fixture app: all
// three pass, including the loading state that is otherwise true for microseconds.
func TestWritePlaywrightSupportHelpers_writesAUsableModule(t *testing.T) {
	dir := supportRepo(t, nil)

	rel, wrote, err := writePlaywrightSupportHelpers(dir)
	if err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if rel != "e2e/support/api.ts" {
		t.Errorf("rel = %q", rel)
	}
	b, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if rerr != nil {
		t.Fatal(rerr)
	}
	got := string(b)
	for _, want := range []string{
		"export async function stubJson(",
		"export async function stubJsonAfter(",
		"export async function stubError(",
		asqsE2EGeneratedHeader,
		// The pattern trap belongs in the file: it is where someone writing a spec will read it.
		"`'**/api/catalog*'`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("module missing %q:\n%s", want, got)
		}
	}
	// It must not be collected as a spec by Playwright's default testMatch.
	if strings.Contains(rel, ".spec.") || strings.Contains(rel, ".test.") {
		t.Errorf("helper path %q would be collected as a test", rel)
	}
}

// A repository's own file is never clobbered; a file this tool wrote is upgraded.
func TestWritePlaywrightSupportHelpers_neverClobbersARepositorysOwn(t *testing.T) {
	const mine = "// my own helpers\nexport const x = 1;\n"
	dir := supportRepo(t, map[string]string{"e2e/support/api.ts": mine})

	if _, _, err := writePlaywrightSupportHelpers(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "e2e", "support", "api.ts"))
	if string(b) != mine {
		t.Errorf("clobbered a repository's own helpers:\n%s", b)
	}

	owned := supportRepo(t, map[string]string{
		"e2e/support/api.ts": "// " + asqsE2EGeneratedHeader + " — safe to edit or delete.\n// stale\n",
	})
	if _, _, err := writePlaywrightSupportHelpers(owned); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(owned, "e2e", "support", "api.ts"))
	if !strings.Contains(string(b2), "stubJson") {
		t.Errorf("an ASQS-owned module was not upgraded:\n%s", b2)
	}
}

// The generator names the module only when it is really there, so a repository that brought its own
// E2E setup — and therefore never ran this bootstrap — is not told to import a file it lacks.
func TestPlaywrightSupportModule_onlyReportsWhatThisToolWrote(t *testing.T) {
	if got := PlaywrightSupportModule(supportRepo(t, nil)); got != "" {
		t.Errorf("reported %q before anything was written", got)
	}
	if got := PlaywrightSupportModule(supportRepo(t, map[string]string{
		"e2e/support/api.ts": "// somebody else's helpers\n",
	})); got != "" {
		t.Errorf("reported a repository's own module as ours: %q", got)
	}

	dir := supportRepo(t, nil)
	if _, _, err := writePlaywrightSupportHelpers(dir); err != nil {
		t.Fatal(err)
	}
	if got := PlaywrightSupportModule(dir); got != "e2e/support/api.ts" {
		t.Errorf("PlaywrightSupportModule = %q, want the written module", got)
	}
	if got := PlaywrightSupportModule(""); got != "" {
		t.Errorf("empty repo path must report nothing, got %q", got)
	}
}
