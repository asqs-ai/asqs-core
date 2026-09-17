package evaluator

import (
	"strings"
	"testing"
)

const inheritedFailureLog = `[xUnit.net 00:00:00.93]     App.FunctionalTests.ApiEndpoints.ContributorList.ReturnsTwo [FAIL]
[xUnit.net 00:00:00.93]       System.ArgumentException : Cannot create an instance of App.FunctionalTests.CustomWebApplicationFactory` + "`" + `1[TProgram] because Type.ContainsGenericParameters is true.
[xUnit.net 00:00:00.93]         at App.FunctionalTests.ApiEndpoints.ContributorList..ctor() in /workspace/tests/App.FunctionalTests/ApiEndpoints/ContributorList.cs:line 12
Failed!  - Failed: 3, Passed: 12, Skipped: 0, Total: 15
`

// The baseline already computes a position-insensitive signature of the test failure the run
// INHERITED, from the full test output. Its only consumers were two audit payload fields — so a run
// that ends test=fail cannot say whether it broke something or merely failed to repair what was
// already red, and that is precisely the question deciding whether it may ship.
func TestBaselineTestFailureRepeated_recognisesTheInheritedFailure(t *testing.T) {
	opts := EvalOptions{Lang: "csharp"}
	opts.BaselineTestSignature = TestFailureSignature(opts.Lang, inheritedFailureLog)

	if !baselineTestFailureRepeated(opts, inheritedFailureLog) {
		t.Fatal("the same failure must be recognised as the one the run inherited")
	}

	// The signature normalises the line and column numbers inside a diagnostic, which is what makes
	// the comparison survive the fixer rewriting the file and moving everything down a few lines.
	shifted := strings.Replace(inheritedFailureLog, "ContributorList.cs:line 12", "ContributorList.cs:line 63", 1)
	if shifted == inheritedFailureLog {
		t.Fatal("fixture does not carry a line number; the normalisation claim is untested")
	}
	if !baselineTestFailureRepeated(opts, shifted) {
		t.Error("the same failure at a different source line must still read as inherited")
	}
}

// A different failure in the same run is NOT the inherited one, however similar. The comparison
// errs towards "this run's problem", which is the safe direction for a claim that could let a
// regression ship.
func TestBaselineTestFailureRepeated_adifferentFailureIsNotInherited(t *testing.T) {
	opts := EvalOptions{Lang: "csharp"}
	opts.BaselineTestSignature = TestFailureSignature(opts.Lang, inheritedFailureLog)

	other := strings.Replace(inheritedFailureLog,
		"System.ArgumentException : Cannot create an instance of",
		"System.NullReferenceException : Object reference not set for", 1)
	if baselineTestFailureRepeated(opts, other) {
		t.Fatal("a different exception in the same file must not read as inherited")
	}
}

// Every branch with nothing to compare must answer "not inherited": a claim that a failure was
// already there is what would let a regression through, so it is made only on evidence.
func TestBaselineTestFailureRepeated_silentWithoutEvidence(t *testing.T) {
	withSig := EvalOptions{Lang: "csharp", BaselineTestSignature: TestFailureSignature("csharp", inheritedFailureLog)}

	if baselineTestFailureRepeated(EvalOptions{Lang: "csharp"}, inheritedFailureLog) {
		t.Error("no baseline signature: nothing to inherit from")
	}
	if baselineTestFailureRepeated(withSig, "") {
		t.Error("no output: nothing to compare")
	}
	if baselineTestFailureRepeated(withSig, "   \n ") {
		t.Error("blank output: nothing to compare")
	}
}

// The baseline is captured per language, and the comparison has to be made on the same terms.
// CanonicalForFixLoop collapses runs of identical lines for C# and for no other language, so a log
// that repeats a failure — which xUnit does, once per test sharing a fixture — normalises to
// different text under the two and must not compare equal.
func TestBaselineTestFailureRepeated_isLanguageScoped(t *testing.T) {
	repeated := strings.Repeat(
		"[xUnit.net 00:00:00.93]       System.ArgumentException : Cannot create an instance.\n", 6)
	log := "[xUnit.net 00:00:00.93]     App.Tests.A.B [FAIL]\n" + repeated +
		"Failed!  - Failed: 6, Passed: 1, Skipped: 0, Total: 7\n"

	csharp := TestFailureSignature("csharp", log)
	java := TestFailureSignature("java", log)
	if csharp == "" || java == "" {
		t.Fatal("no signature computed")
	}
	if csharp == java {
		t.Fatal("C# collapses repeated lines and Java does not; the signatures must differ")
	}

	opts := EvalOptions{Lang: "java", BaselineTestSignature: csharp}
	if baselineTestFailureRepeated(opts, log) {
		t.Error("a signature taken under one language matched under another")
	}
}
