package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator/errclass"
)

// The protection U7 actually wanted: an E2E failure caused by absent browsers must classify as an
// execution kind, so the fix loop stops instead of asking the LLM to repair test code that is fine.
// Matching the real output has no false positives, which a cache-directory prediction cannot claim.
func TestErrclass_missingBrowsersClassifyAsExecutionFailure(t *testing.T) {
	outputs := map[string]string{
		"playwright js": `browserType.launch: Executable doesn't exist at /home/ci/.cache/ms-playwright/chromium-1091/chrome-linux/chrome
╔══════════════════════════════════════════════════════╗
║ Looks like Playwright Test was just installed or updated.
║ Please run the following command to download new browsers:
║     npx playwright install
╚══════════════════════════════════════════════════════╝`,
		"playwright dotnet": `Microsoft.Playwright.PlaywrightException : Executable doesn't exist at /root/.cache/ms-playwright/chromium-1091/chrome-linux/chrome`,
		"playwright deps":   `Host system is missing dependencies to run browsers. Please install them with the following command: sudo npx playwright install-deps`,
		"cypress":           `The cypress npm package is installed, but the Cypress binary is missing.`,
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			got := errclass.Kind("typescript", out)
			if got != errclass.KindBrowsersMissing {
				t.Fatalf("Kind = %q, want %q", got, errclass.KindBrowsersMissing)
			}
			if !errclass.IsHostExecutionKind(got) {
				t.Error("missing browsers must count as an execution kind so the fix loop stops")
			}
			if r := errclass.Remediation(got); !strings.Contains(r, "bootstrap.e2e_framework.enabled") {
				t.Errorf("remediation %q should name the config key that fixes it", r)
			}
		})
	}
}

// errclass prefers false negatives: an ordinary failing test that merely mentions a browser must
// not be classified as infrastructure, or a real bug would silently skip the fixer.
func TestErrclass_ordinaryBrowserTestFailureIsNotClassified(t *testing.T) {
	for _, out := range []string{
		"FAIL src/login.spec.ts > logs in with chromium\n  expected 'Welcome' to equal 'Hello'",
		"AssertionError: expected browser title to be 'Dashboard'",
		"Error: page.click: Timeout 30000ms exceeded waiting for selector '#submit'",
	} {
		if got := errclass.Kind("typescript", out); got == errclass.KindBrowsersMissing {
			t.Errorf("ordinary failure misclassified as missing browsers:\n%s", out)
		}
	}
}

func TestE2EFrameworkNeedsBrowsers(t *testing.T) {
	for _, fw := range []string{"playwright", "playwright-java", "playwright-dotnet", "cypress", "PLAYWRIGHT"} {
		if !e2eFrameworkNeedsBrowsers(fw) {
			t.Errorf("%q drives a real browser", fw)
		}
	}
	for _, fw := range []string{"", "selenium", "failsafe", "junit"} {
		if e2eFrameworkNeedsBrowsers(fw) {
			t.Errorf("%q should not trigger the browser preflight", fw)
		}
	}
}

func TestWarnLocalE2EBrowsersMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	sb := &Sandbox{Type: "local"}

	out := captureStderr(t, func() { sb.warnLocalE2EBrowsersMissing("typescript", "playwright") })
	for _, want := range []string{"E2E preflight", "playwright browsers are not installed", "bootstrap.e2e_framework.enabled", "general.sandbox.type to docker"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning %q missing %q", out, want)
		}
	}
	// It must not read as a hard stop: a repo driving the system browser needs no cache.
	if !strings.Contains(out, "Continuing") {
		t.Errorf("warning %q should make clear the run continues", out)
	}
	if again := captureStderr(t, func() { sb.warnLocalE2EBrowsersMissing("typescript", "playwright") }); strings.TrimSpace(again) != "" {
		t.Errorf("warning repeated: %q", again)
	}
}

func TestWarnLocalE2EBrowsersMissing_silentWhenPresentOrIrrelevant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")

	// A framework that drives no browser.
	if got := captureStderr(t, func() { (&Sandbox{Type: "local"}).warnLocalE2EBrowsersMissing("java", "failsafe") }); strings.TrimSpace(got) != "" {
		t.Errorf("non-browser framework should warn nothing, got %q", got)
	}

	// Browsers present.
	cache := playwrightBrowserCacheDir()
	if err := os.MkdirAll(filepath.Join(cache, "chromium-1091"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := captureStderr(t, func() { (&Sandbox{Type: "local"}).warnLocalE2EBrowsersMissing("typescript", "playwright") }); strings.TrimSpace(got) != "" {
		t.Errorf("installed browsers should warn nothing, got %q", got)
	}
}

// An empty cache directory is as useless as an absent one, and is what a half-finished install
// leaves behind.
func TestDirHasEntries(t *testing.T) {
	empty := t.TempDir()
	if dirHasEntries(empty) {
		t.Error("an empty directory is not an installed browser cache")
	}
	if err := os.Mkdir(filepath.Join(empty, "chromium"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !dirHasEntries(empty) {
		t.Error("a populated directory should count")
	}
	if dirHasEntries(filepath.Join(empty, "does-not-exist")) {
		t.Error("an absent directory should not count")
	}
}

// Both targets announce the E2E pass under the same label, so the two test passes of one round are
// told apart in the log on either runner.
func TestE2EPass_usesTheE2ELabelOnBothTargets(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	repo := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)

	local := captureStderr(t, func() {
		(&Sandbox{Type: "local", Timeout: "30s"}).
			TestE2EPass(context.Background(), repo, "java", "mvn -q -B failsafe:integration-test", "playwright-java")
	})
	if !strings.Contains(local, "Tests (E2E)") {
		t.Errorf("local E2E pass should announce the E2E label:\n%s", local)
	}

	dsb, drepo := fakeDockerSandbox(t, "30s", "exit 0")
	docker := captureStderr(t, func() {
		dsb.TestE2EPass(context.Background(), drepo, "java", "mvn -q -B failsafe:integration-test", "playwright-java")
	})
	if !strings.Contains(docker, "Tests (E2E)") {
		t.Errorf("docker E2E pass should announce the E2E label:\n%s", docker)
	}
}
