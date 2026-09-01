package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePkg(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The regression for run api-f1d4227cb6db875a2e51c3100b3e1be8: bootstrap installed Jest, verified it
// by invoking it directly, wrote its runner to `test:asqs` — and the eval then ran `npm test`, which
// in that repository was `echo "no unit/e2e runners configured in this ASQS fixture"`. 284 ms, exit
// 0, "tests ok", ten generated Angular tests never executed.
func TestResolveToolchain_runsTheBootstrapInstalledScript(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `{
	  "scripts": {
	    "test": "echo \"no unit/e2e runners configured in this ASQS fixture\"",
	    "test:asqs": "jest"
	  }
	}`)

	p, err := ResolveToolchain(dir, "typescript", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	test := strings.Join(p.Test, " ")
	if !strings.Contains(test, "npm run test:asqs") {
		t.Errorf("Test = %q, want it to run the script bootstrap verified", test)
	}
	if strings.Contains(test, "npm test") {
		t.Errorf("Test = %q, must not fall back to the repository's own test script", test)
	}
	// The coverage fallback runs the same suite, so it has to agree.
	if cov := strings.Join(p.Coverage, " "); !strings.Contains(cov, "npm run test:asqs") {
		t.Errorf("Coverage = %q, want its fallback to use the bootstrap script too", cov)
	}
}

// Without the script nothing changes: a repository whose own runner was already complete is skipped
// by bootstrap before any package.json edit, and `npm test` stays the right question to ask.
func TestResolveToolchain_withoutBootstrapScriptKeepsNpmTest(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, `{"scripts": {"test": "jest"}}`)

	p, err := ResolveToolchain(dir, "typescript", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if test := strings.Join(p.Test, " "); !strings.Contains(test, "CI=true npm test") {
		t.Errorf("Test = %q, want the historical npm test invocation", test)
	}
}

func TestBootstrapTestScript(t *testing.T) {
	t.Run("absent package.json", func(t *testing.T) {
		if got := bootstrapTestScript(t.TempDir()); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("empty repo path", func(t *testing.T) {
		if got := bootstrapTestScript(""); got != "" {
			t.Errorf("got %q, want empty: BuiltinToolchain passes no repo and must keep its argv", got)
		}
	})
	t.Run("unparseable package.json", func(t *testing.T) {
		dir := t.TempDir()
		writePkg(t, dir, `{ not json`)
		if got := bootstrapTestScript(dir); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("script present but empty", func(t *testing.T) {
		dir := t.TempDir()
		writePkg(t, dir, `{"scripts": {"test:asqs": "   "}}`)
		if got := bootstrapTestScript(dir); got != "" {
			t.Errorf("got %q, want empty: a blank script would run nothing", got)
		}
	})
	t.Run("script present", func(t *testing.T) {
		dir := t.TempDir()
		writePkg(t, dir, `{"scripts": {"test:asqs": "vitest run"}}`)
		if got := bootstrapTestScript(dir); got != asqsBootstrapTestScript {
			t.Errorf("got %q, want %q", got, asqsBootstrapTestScript)
		}
	})
}

// pnpm and yarn repositories get the same treatment; the package manager is detected from the
// lockfile, so the fixtures need one.
func TestResolveToolchain_bootstrapScriptAcrossPackageManagers(t *testing.T) {
	cases := []struct {
		name     string
		lockfile string
		want     string
	}{
		{"pnpm", "pnpm-lock.yaml", "pnpm run test:asqs"},
		{"yarn", "yarn.lock", "yarn run test:asqs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePkg(t, dir, `{"scripts": {"test": "echo nothing", "test:asqs": "jest"}}`)
			if err := os.WriteFile(filepath.Join(dir, tc.lockfile), []byte("\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := ResolveToolchain(dir, "typescript", "", "", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if test := strings.Join(p.Test, " "); !strings.Contains(test, tc.want) {
				t.Errorf("Test = %q, want %q", test, tc.want)
			}
		})
	}
}

// BuiltinToolchain has no repository to inspect and must keep its historical argv.
func TestBuiltinToolchain_keepsNpmTestWithoutARepo(t *testing.T) {
	p := BuiltinToolchain(TypeScriptNPM, "", "", "", "")
	if test := strings.Join(p.Test, " "); !strings.Contains(test, "CI=true npm test") {
		t.Errorf("Test = %q, want the no-repo default", test)
	}
}
