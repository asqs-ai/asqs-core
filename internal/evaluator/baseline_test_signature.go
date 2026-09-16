package evaluator

import (
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// reTestExcerptHeader matches the header errout.ExtractTestFailureBlocks prepends, which counts the
// lines of the log it read. That count is the one part of the excerpt guaranteed to move when a run
// adds tests — 311 of 1049 became 311 of 1060 in api-60c974ecfa42c5bee4930a7e590e4548 — so hashing
// it would reintroduce the whole defect one layer down.
var reTestExcerptHeader = regexp.MustCompile(`^\[test-failure excerpt:[^\]]*\]\n?`)

// TestFailureSignature identifies a test failure by the FAILURES in the log, not by the log.
//
// FailureSignature over raw test output is the wrong instrument for this comparison, and run
// api-60c974ecfa42c5bee4930a7e590e4548 shows why: its baseline log was 1049 lines and its final log
// 1060, because the run had added the passing tests it was there to write. The sanitiser extracted
// exactly 311 failure lines from each — the same three failures, in two files the run never
// authored — and the two hashes differed anyway. All five evaluator.test rows reported
// failure_inherited=false. Since adding tests is the entire job, the log always grows, so the
// comparison could never once return true in the system it was built for.
//
// errout.ExtractTestFailureBlocks is the same sanitiser the fix loop's prompt and the baseline's own
// summary already use, so all three describe a failure the same way.
//
// The raw output is the fallback for a log the extractor does not recognise. Falling silent there
// would give up the one case the old comparison did handle — two identical opaque logs — to fix the
// case it did not.
func TestFailureSignature(lang, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	blocks := reTestExcerptHeader.ReplaceAllString(errout.ExtractTestFailureBlocks(output), "")
	if blocks = strings.TrimSpace(blocks); blocks != "" {
		return FailureSignature(lang, StepTest, blocks)
	}
	return FailureSignature(lang, StepTest, output)
}

// baselineTestFailureRepeated reports whether a failing test step is the failure this run inherited
// rather than one it caused.
//
// The comparison is between two signatures taken the same way — FailureSignature normalises line
// numbers, paths and ordering — so a failure reported at a different position in a longer log still
// matches, while a different failure in the same file does not.
//
// Every branch with nothing to compare answers false. A wrong "true" here would let a regression
// ship under the previous run's failure, so the claim is made only on evidence.
func baselineTestFailureRepeated(opts EvalOptions, output string) bool {
	if strings.TrimSpace(opts.BaselineTestSignature) == "" || strings.TrimSpace(output) == "" {
		return false
	}
	return TestFailureSignature(opts.Lang, output) == opts.BaselineTestSignature
}
