package pipeline

import (
	"testing"

	"github.com/asqs/asqs-core/internal/runner"
)

func TestFormatSkipReason(t *testing.T) {
	if got := formatSkipReason(runner.FormatResolveResult{SkipReason: "no per-file formatter for java (google-java-format not on PATH)"}); got != "no per-file formatter for java (google-java-format not on PATH)" {
		t.Fatalf("got %q", got)
	}
	if got := formatSkipReason(runner.FormatResolveResult{}); got != "no formatter resolved" {
		t.Fatalf("got %q", got)
	}
}
