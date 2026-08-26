package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Local E2E browser preflight (D4, U7).
//
// The Docker target swaps in a Playwright image, which ships browsers and their OS dependencies.
// The local target has no image to swap, so the host must already have them —
// e2e_framework_bootstrap installs them when enabled (it runs `playwright install`), and when it is
// not enabled nothing does.
//
// # Why this warns rather than fails
//
// The plan called for a hard failure. An absent browser cache does not prove the run will fail:
// Playwright configured with `channel: "chrome"` drives the system Chrome and downloads nothing, so
// failing on a missing ~/.cache/ms-playwright would block a perfectly working setup. That is a
// false positive on a check that cannot see the repository's Playwright config.
//
// The protection the plan actually wanted — stopping the fix loop from burning its budget on
// something no generated test can repair — is delivered instead by errclass.KindBrowsersMissing,
// which matches what Playwright and Cypress print when the browsers really are absent. That has no
// false positives, because it fires on the actual failure rather than a prediction of it. This
// preflight is what remains: an early, specific note that names the config key which fixes it.

// e2eFrameworkNeedsBrowsers reports whether a framework drives a real browser.
func e2eFrameworkNeedsBrowsers(fw string) bool {
	switch strings.ToLower(strings.TrimSpace(fw)) {
	case "playwright", "playwright-java", "playwright-dotnet", "cypress":
		return true
	}
	return false
}

// playwrightBrowserCacheDir returns the directory Playwright downloads browsers into.
func playwrightBrowserCacheDir() string {
	if p := strings.TrimSpace(os.Getenv("PLAYWRIGHT_BROWSERS_PATH")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "ms-playwright")
	case "windows":
		return filepath.Join(home, "AppData", "Local", "ms-playwright")
	default:
		return filepath.Join(home, ".cache", "ms-playwright")
	}
}

// cypressBinaryCacheDir returns the directory the Cypress binary is unpacked into.
func cypressBinaryCacheDir() string {
	if p := strings.TrimSpace(os.Getenv("CYPRESS_CACHE_FOLDER")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "Cypress")
	case "windows":
		return filepath.Join(home, "AppData", "Local", "Cypress")
	default:
		return filepath.Join(home, ".cache", "Cypress")
	}
}

// dirHasEntries reports whether dir exists and contains at least one entry. An empty cache
// directory is as useless as an absent one and is what a half-finished install leaves behind.
func dirHasEntries(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// warnLocalE2EBrowsersMissing notes, once per run, that a browser-driving E2E pass is about to run
// on a host with no browser cache.
func (s *Sandbox) warnLocalE2EBrowsersMissing(lang, e2eFramework string) {
	if !e2eFrameworkNeedsBrowsers(e2eFramework) {
		return
	}
	cache, tool := playwrightBrowserCacheDir(), "playwright"
	if strings.EqualFold(strings.TrimSpace(e2eFramework), "cypress") {
		cache, tool = cypressBinaryCacheDir(), "cypress"
	}
	if dirHasEntries(cache) {
		return
	}
	s.runState().e2eBrowserWarnOnce.Do(func() {
		where := cache
		if where == "" {
			where = "(could not determine the cache directory for this user)"
		}
		fmt.Fprintf(os.Stderr,
			"[asqs-eval] E2E preflight: %s browsers are not installed for this user (%s is empty or absent), "+
				"and runner.type is local so there is no image to supply them.\n"+
				"  Enable runner.e2e_framework_bootstrap.enabled to have ASQS install them, install them by hand for the "+
				"account ASQS runs as, or set runner.type to docker.\n"+
				"  Continuing: a repository that drives the system browser (Playwright `channel`) does not need this cache.\n",
			tool, where)
	})
}
