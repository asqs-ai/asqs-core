package runner

import (
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// failedStepSummaryLines bounds the failure-excerpt summary. Larger than the old firstLines(out, 5)
// because excerpt lines are all signal; still small enough for an audit row and a stderr echo.
const failedStepSummaryLines = 12

// failedStepSummary derives the Summary for a FAILED eval step.
//
// For test-shaped steps it prefers the failure-block excerpt over the raw head: a Spring Boot
// surefire run opens with hundreds of INFO lines, so firstLines(out, 5) audited exactly none of
// the failures — run api-12aa1935d113c9ea8b50a516fd275660's evaluator.test rows carried 767 chars
// of context-loader noise across twelve failing iterations, and the post-mortem had to
// reconstruct the actual failures from surefire XML files. The full output still travels in
// StepResult.Output; this only changes which slice of it the summary shows.
//
// Logs without a recognisable failure marker, and every non-test step, keep the previous
// firstLines head byte-for-byte.
func failedStepSummary(stepEval evaluator.SandboxStep, out string, headLines int) string {
	if stepEval == evaluator.StepTest || stepEval == evaluator.StepTestE2E {
		if excerpt := errout.ExtractTestFailureBlocks(out); excerpt != "" {
			return firstLines(excerpt, failedStepSummaryLines)
		}
	}
	s := firstLines(out, headLines)
	if s == "" {
		return "failed"
	}
	return s
}
