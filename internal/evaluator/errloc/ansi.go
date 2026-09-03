package errloc

import "regexp"

// ansiEscape matches the terminal escape sequences a coloured test runner emits: CSI sequences
// (`ESC [ … final-byte`, which covers SGR colour/bold/dim and cursor movement) and OSC sequences
// (`ESC ] … BEL` or `ESC ] … ESC \`, used for hyperlinks and titles). Bare single-byte escapes are
// left alone; nothing that reaches a build log uses them.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// StripANSI removes terminal colour and control sequences from captured step output.
//
// Every parser in this chain assumes plain text, and none of it was: the sandbox sets CI=true on
// every step (internal/runner baseStepEnv), and vitest's colour library treats CI as "colour is
// wanted". Run api-72dad6bb281cacee338f43c48432a780 (2026-09-03) captured
//
//	src/app/router.test.tsx:\x1b[2m59:24\x1b[22m
//	\x1b[32m✓\x1b[39m src/app/AppLayout.test.tsx (4 tests)
//	\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m src/pages/OrdersPage.test.tsx
//
// so reFileLineColon never saw a line number after the colon, the `✓ ` pass-line filter never saw
// a line starting with ✓, and the `FAIL ` / `❯ ` prefix checks never fired. The consequences were
// a fixer scope narrowed to the one file cited by an uncoloured React warning, two fully passing
// generated tests discarded as failing, and a failure excerpt made of node_modules frames only.
//
// It is called where the output is captured (docker and local step runners) so every consumer sees
// clean text, and again at the entry of ParseLocations, ParseFailingTestPaths and
// ExtractTestFailureBlocks so a fixture or a caller that bypasses the runner cannot regress this.
// Idempotent and cheap on plain text: the regexp finds nothing and the input is returned as is.
func StripANSI(s string) string {
	if s == "" {
		return s
	}
	return ansiEscape.ReplaceAllString(s, "")
}
