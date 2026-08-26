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
