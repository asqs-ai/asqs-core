package errout

import (
	"strings"
	"testing"
)

func TestExtractTestFailureBlocks_capsStackFrames(t *testing.T) {
	var b strings.Builder
	b.WriteString("java.lang.IllegalStateException: boom\n")
	for i := 0; i < 40; i++ {
		b.WriteString("\tat com.example.Deep.frame(Deep.java:1)\n")
	}
	got := ExtractTestFailureBlocks(b.String())
	if n := strings.Count(got, "Deep.java:1"); n != maxFramesPerFailureBlock {
		t.Errorf("kept %d frames, want %d", n, maxFramesPerFailureBlock)
	}
}

// "" for marker-free logs is the contract both consumers' fallbacks hang on: the fixer gist falls
// back to head+tail, the runner summary to firstLines.
func TestExtractTestFailureBlocks_noMarkersReturnsEmpty(t *testing.T) {
	if got := ExtractTestFailureBlocks("plain output\nnothing failing here\n"); got != "" {
		t.Errorf("marker-free log must return \"\", got:\n%s", got)
	}
}

func TestExtractTestFailureBlocks_keepsFailureAndDropsNoise(t *testing.T) {
	log := "INFO boot noise\n" +
		"[ERROR] shouldWork  Time elapsed: 0.04 s  <<< FAILURE!\n" +
		"org.opentest4j.AssertionFailedError: expected: <1> but was: <2>\n" +
		"\tat com.example.FooTests.shouldWork(FooTests.java:12)\n" +
		"INFO shutdown noise\n"
	got := ExtractTestFailureBlocks(log)
	for _, want := range []string{"AssertionFailedError", "FooTests.java:12", "[test-failure excerpt:"} {
		if !strings.Contains(got, want) {
			t.Errorf("excerpt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "INFO boot noise") {
		t.Errorf("excerpt kept noise:\n%s", got)
	}
}

// The FAIL / ❯ markers are prefix checks. Coloured vitest output puts an escape code in front of
// both, so the excerpt in run api-72dad6bb281cacee338f43c48432a780 consisted of node_modules
// stack frames only and named not one failing test.
func TestExtractTestFailureBlocks_colouredVitestFailBlocks(t *testing.T) {
	log := " \x1b[32m✓\x1b[39m src/app/AppLayout.test.tsx \x1b[2m(\x1b[22m\x1b[2m4 tests\x1b[22m\x1b[2m)\x1b[22m\n" +
		"stdout | noise line\n" +
		"\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m src/app/router.test.tsx\x1b[2m > \x1b[22mrouter\x1b[2m > \x1b[22mshould create a browser router\n" +
		"Error: Cannot find module './router'\n" +
		"\x1b[36m \x1b[2m❯\x1b[22m src/app/router.test.tsx:\x1b[2m59:24\x1b[22m\x1b[39m\n"
	got := ExtractTestFailureBlocks(log)
	if got == "" {
		t.Fatal("coloured vitest FAIL block not recognised as a failure marker")
	}
	if !strings.Contains(got, "FAIL  src/app/router.test.tsx") {
		t.Errorf("excerpt lost the FAIL line:\n%s", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("excerpt still carries escape codes:\n%s", got)
	}
	if strings.Contains(got, "AppLayout.test.tsx") {
		t.Errorf("a passing file must not appear in the failure excerpt:\n%s", got)
	}
}
