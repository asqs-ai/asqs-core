package testbootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exact output of run api-baecdffd823e00255932b3f676a00caf (2026-09-03 14:59): node:22's npm
// 10.9.8 died in its own peer-set loader after vitest 5.0.0 was published, on a manifest that had
// installed cleanly the same morning.
const npmEdgesOutCrash = "npm error Cannot read properties of null (reading 'edgesOut')\nnpm error A complete log of this run can be found in: /root/.npm/_logs/2026-09-03T14_59_19_007Z-debug-0.log\n"

func TestNpmArboristCrash(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want bool
	}{
		"edgesOut_npm10":       {npmEdgesOutCrash, true},
		"undefined_typeerror":  {"npm ERR! TypeError: Cannot read properties of undefined (reading 'version')\n", true},
		"registry_404":         {"npm error code E404\nnpm error 404 Not Found - GET https://registry.npmjs.org/nope\n", false},
		"peer_conflict":        {"npm error code ERESOLVE\nnpm error ERESOLVE unable to resolve dependency tree\n", false},
		"ordinary_success":     {"added 296 packages in 8s\n", false},
		"message_not_from_npm": {"TypeError: Cannot read properties of null (reading 'x') at app.js:3\n", false},
		"empty":                {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := npmArboristCrash([]byte(tc.out)); got != tc.want {
				t.Fatalf("npmArboristCrash = %v, want %v for:\n%s", got, tc.want, tc.out)
			}
		})
	}
}

func TestNpmFallbackInstallArgv(t *testing.T) {
	got := npmFallbackInstallArgv([]string{"npm", "install", "--no-audit"})
	want := "npx --yes " + npmFallbackSpec + " install --no-audit"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	if npmFallbackInstallArgv(nil) != nil {
		t.Fatal("empty argv must stay empty")
	}
}

// stubBin puts a shell script named `name` first on PATH and returns the directory.
func stubBin(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// End to end on the host path: npm crashes, npx runs the pinned npm with the same arguments, the
// install succeeds, and the output carries the retry marker for the caller's audit.
func TestRunPackageManagerInstall_retriesNpmCrashThroughNpx(t *testing.T) {
	bin := t.TempDir()
	repo := t.TempDir()
	argLog := filepath.Join(t.TempDir(), "npx.args")
	stubBin(t, bin, "npm", `printf '%s' "`+strings.ReplaceAll(npmEdgesOutCrash, "\n", `\n`)+`"; exit 1`)
	stubBin(t, bin, "npx", `printf '%s\n' "$*" > "`+argLog+`"; echo "added 5 packages"; exit 0`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := RunPackageManagerInstall(context.Background(), nil, repo, PMNpm, true, true, true, nil)
	if err != nil {
		t.Fatalf("the retry succeeded, so the install must succeed: %v\n%s", err, out)
	}
	if !installOutputRetried(out) {
		t.Fatalf("output must carry the retry marker:\n%s", out)
	}
	if !strings.Contains(string(out), "edgesOut") || !strings.Contains(string(out), "added 5 packages") {
		t.Errorf("output must keep both the crash and the retry's output:\n%s", out)
	}
	args, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatal("npx was never invoked")
	}
	if got := strings.TrimSpace(string(args)); got != "--yes "+npmFallbackSpec+" install" {
		t.Errorf("npx args = %q, want the same install through the pinned npm", got)
	}
}

// A crash on the retry too is reported as the retry's failure; an ordinary failure never retries.
func TestRunPackageManagerInstall_doesNotRetryOrdinaryFailures(t *testing.T) {
	bin := t.TempDir()
	repo := t.TempDir()
	stubBin(t, bin, "npm", `echo "npm error code E404"; echo "npm error 404 Not Found - GET https://registry.npmjs.org/nope"; exit 1`)
	stubBin(t, bin, "npx", `echo "npx must not run"; exit 99`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := RunPackageManagerInstall(context.Background(), nil, repo, PMNpm, true, true, true, nil)
	if err == nil {
		t.Fatal("an E404 must fail the install")
	}
	if installOutputRetried(out) || strings.Contains(string(out), "npx must not run") {
		t.Fatalf("a registry error must not trigger the npm fallback:\n%s", out)
	}
}

// pnpm and yarn have their own resolvers; the fallback is npm-only.
func TestRunPackageManagerInstall_fallbackIsNpmOnly(t *testing.T) {
	bin := t.TempDir()
	repo := t.TempDir()
	stubBin(t, bin, "yarn", `printf '%s' "`+strings.ReplaceAll(npmEdgesOutCrash, "\n", `\n`)+`"; exit 1`)
	stubBin(t, bin, "npx", `echo "npx must not run"; exit 99`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := RunPackageManagerInstall(context.Background(), nil, repo, PMYarn, true, true, true, nil)
	if err == nil || installOutputRetried(out) {
		t.Fatalf("yarn failures must not be retried through npm; err=%v out=%s", err, out)
	}
}
