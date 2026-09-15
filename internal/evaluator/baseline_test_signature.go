package evaluator

import "strings"

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
	return FailureSignature(opts.Lang, StepTest, output) == opts.BaselineTestSignature
}
