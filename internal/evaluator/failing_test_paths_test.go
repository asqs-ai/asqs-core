package evaluator

import "testing"

// Discard attribution runs ParseFailingTestPaths over the raw step output and relies on
// isObviousPassSummaryLine dropping `✓ file` lines. vitest colours the tick, so the line starts
// with an escape code, the filter missed it, and run api-72dad6bb281cacee338f43c48432a780
// discarded src/app/AppLayout.test.tsx and src/pages/settings/SettingsLayout.test.tsx — both of
// which had passed every test.
func TestParseFailingTestPaths_colouredVitestOutput(t *testing.T) {
	out := " \x1b[31m❯\x1b[39m src/app/router.test.tsx \x1b[2m(\x1b[22m\x1b[2m7 tests\x1b[22m\x1b[2m | \x1b[22m\x1b[31m7 failed\x1b[39m\x1b[2m)\x1b[22m\n" +
		" \x1b[32m✓\x1b[39m src/app/AppLayout.test.tsx \x1b[2m(\x1b[22m\x1b[2m4 tests\x1b[22m\x1b[2m)\x1b[22m\n" +
		"\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m src/app/router.test.tsx\x1b[2m > \x1b[22mrouter\n" +
		"\x1b[36m \x1b[2m❯\x1b[22m src/app/router.test.tsx:\x1b[2m59:24\x1b[22m\x1b[39m\n"
	artifacts := []string{"src/app/router.test.tsx", "src/app/AppLayout.test.tsx"}
	got := ParseFailingTestPaths(out, artifacts)
	if len(got) != 1 || got[0] != "src/app/router.test.tsx" {
		t.Fatalf("want only the failing file; got %v", got)
	}
}

// The colour fix above was not the whole story. vitest prints a BANNER naming the file whenever a
// test writes to stdout or stderr — passing or failing — and that banner carries the file's full
// path:
//
//	stderr | src/pages/HomePage.test.tsx > HomePage > should render the home page
//	⚠️ React Router Future Flag Warning: …
//
// Once isObviousPassSummaryLine has dropped that file's `✓` line, the banner is the ONLY surviving
// mention of it, and ParseFailingTestPaths' path containment check matches it. An asqs-go run
// attributed src/pages/HomePage.test.tsx — 3 tests, all green — to a router.test.tsx failure on
// the strength of one React Router deprecation warning, offered it to the fixer as writable, and
// then deleted it in the discard promotion.
func TestParseFailingTestPaths_vitestStreamBannerIsNotAFailure(t *testing.T) {
	out := " ❯ src/app/router.test.tsx (8 tests | 8 failed) 1051ms\n" +
		"     × should render the HomePage when navigating to root / 21ms\n" +
		"stderr | src/pages/HomePage.test.tsx > HomePage > should render the home page and display welcome content\n" +
		"⚠️ React Router Future Flag Warning: React Router will begin wrapping state updates in `React.startTransition` in v7.\n" +
		"\n" +
		" ✓ src/pages/HomePage.test.tsx (3 tests) 102ms\n" +
		" FAIL  src/app/router.test.tsx > App Router > should render the HomePage when navigating to root /\n"
	artifacts := []string{"src/app/router.test.tsx", "src/pages/HomePage.test.tsx"}
	got := ParseFailingTestPaths(out, artifacts)
	if len(got) != 1 || got[0] != "src/app/router.test.tsx" {
		t.Fatalf("want only the failing file; got %v", got)
	}
}

// The banner is neutral evidence, not exculpatory: a file that both writes to stderr AND fails is
// still named by its `❯ … | n failed` line and its FAIL header, so dropping the banner must not
// lose it.
func TestParseFailingTestPaths_vitestStreamBannerKeepsRealFailures(t *testing.T) {
	out := "stdout | src/pages/OrdersPage.test.tsx > OrdersPage > should display a loading state\n" +
		"fetching…\n" +
		" ❯ src/pages/OrdersPage.test.tsx (5 tests | 4 failed) 3109ms\n" +
		" FAIL  src/pages/OrdersPage.test.tsx > OrdersPage > should display a loading state when fetching orders\n"
	got := ParseFailingTestPaths(out, []string{"src/pages/OrdersPage.test.tsx"})
	if len(got) != 1 || got[0] != "src/pages/OrdersPage.test.tsx" {
		t.Fatalf("want the failing file; got %v", got)
	}
}
