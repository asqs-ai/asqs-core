package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The commit SHA must come from the CHECKOUT'S HEAD, and this guard exists because the alternative
// failed silently for weeks upstream.
//
// `pipeline.Options.CommitSHA` was plumbed all the way to `symbol_versions` while nothing ever set
// it. Every version row was written against the empty commit, so churn —
// `count(DISTINCT body_hash)` per symbol over a window — could never count past one, and the whole
// history table looked populated while carrying no signal at all. Nothing failed; a feature just
// quietly did not work.
//
// A source guard rather than a behavioural test because the failure is an ABSENCE: no output is
// wrong, no error is raised, and the only observable is a column that is empty everywhere. What
// there is to assert is that the wiring exists.
func TestCommitSHAIsWiredFromHead(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	if !strings.Contains(src, "gitRepo.HeadSHA()") {
		t.Error("main.go never calls gitRepo.HeadSHA(); symbol history would be recorded against the " +
			"empty commit and churn could never count past one")
	}
	if !regexp.MustCompile(`CommitSHA:\s*commitSHA`).MatchString(src) {
		t.Error("the resolved SHA is not passed as pipeline.Options.CommitSHA, so nothing downstream sees it")
	}
	// A plain directory is a supported way to run, so a missing HEAD must not fail the run.
	if !strings.Contains(src, "if gitRepo != nil {") {
		t.Error("HeadSHA is not guarded on gitRepo being non-nil; running against a non-git folder would panic")
	}
}
