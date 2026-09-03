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
