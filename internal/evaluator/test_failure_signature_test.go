package evaluator

import (
	"strings"
	"testing"
)

// The three failures run api-60c974ecfa42c5bee4930a7e590e4548 ended on, as the log renders them.
const inheritedBlocks = `[xUnit.net 00:00:03.10]     Clean.Architecture.FunctionalTests.ApiEndpoints.ContributorList.ReturnsTwoContributors [FAIL]
[xUnit.net 00:00:03.10]       System.ArgumentException : Format of the initialization string does not conform to specification starting at index 0.
[xUnit.net 00:00:03.10]         at System.Data.Common.DbConnectionOptions.GetKeyValuePair(String connectionString)
[xUnit.net 00:00:03.11]     Clean.Architecture.FunctionalTests.ApiEndpoints.ContributorGetById.ReturnsNotFoundGivenId1000 [FAIL]
[xUnit.net 00:00:03.11]       System.Net.Http.HttpRequestException : Expected 404 NotFound but was 500 InternalServerError
`

// baselineLog is the pre-generation run: the failures above and a handful of passing tests.
func baselineLog() string {
	return "Test run for /workspace/tests/App.UnitTests/bin/Release/net10.0/App.UnitTests.dll\n" +
		"A total of 1 test files matched the specified pattern.\n" +
		inheritedBlocks +
		"Failed!  - Failed: 3, Passed: 6, Skipped: 1, Total: 10\n"
}

// afterGenerationLog is the same suite after this run added tests: the SAME failures, more passes.
func afterGenerationLog() string {
	return "Test run for /workspace/tests/App.UnitTests/bin/Release/net10.0/App.UnitTests.dll\n" +
		"A total of 1 test files matched the specified pattern.\n" +
		"[xUnit.net 00:00:02.01]     App.FunctionalTests.SeedDataTests.SeedsContributors [PASS]\n" +
		"[xUnit.net 00:00:02.40]     App.FunctionalTests.ExtensionsTests.MapsEndpoints [PASS]\n" +
		inheritedBlocks +
		"Failed!  - Failed: 3, Passed: 17, Skipped: 1, Total: 21\n"
}

// The comparison hashed the WHOLE test log. ASQS adds tests, so the log always grows — run
// api-60c974ecfa42c5bee4930a7e590e4548 went from 1049 lines to 1060 while the sanitiser extracted
// exactly 311 failure lines from each — and the signature therefore always differed. All five
// evaluator.test rows reported failure_inherited=false for failures that were, every one of them,
// in files the run never authored.
//
// A signature of a FAILURE has to be taken from the failure.
func TestTestFailureSignature_survivesTestsBeingAdded(t *testing.T) {
	base := TestFailureSignature("csharp", baselineLog())
	after := TestFailureSignature("csharp", afterGenerationLog())

	if base == "" {
		t.Fatal("no signature computed for the baseline log")
	}
	if base != after {
		t.Errorf("the same failures hashed differently once passing tests were added:\n  baseline %s\n  after    %s", base, after)
	}
}

// And the whole point of the comparison: it must now answer true where it answered false.
func TestBaselineTestFailureRepeated_recognisesInheritedFailuresInALongerLog(t *testing.T) {
	opts := EvalOptions{Lang: "csharp"}
	opts.BaselineTestSignature = TestFailureSignature(opts.Lang, baselineLog())

	if !baselineTestFailureRepeated(opts, afterGenerationLog()) {
		t.Error("failures the baseline recorded must read as inherited even when the run added passing tests")
	}
}

// A failure the run introduced, on top of the inherited ones, is not the same failure set.
func TestBaselineTestFailureRepeated_aNewFailureAlongsideInheritedOnesIsNotInherited(t *testing.T) {
	opts := EvalOptions{Lang: "csharp"}
	opts.BaselineTestSignature = TestFailureSignature(opts.Lang, baselineLog())

	withNew := strings.Replace(afterGenerationLog(),
		"Failed!  - Failed: 3",
		"[xUnit.net 00:00:02.80]     App.FunctionalTests.SeedDataTests.SeedsContributors [FAIL]\n"+
			"[xUnit.net 00:00:02.80]       System.NullReferenceException : Object reference not set to an instance of an object.\n"+
			"Failed!  - Failed: 4", 1)

	if baselineTestFailureRepeated(opts, withNew) {
		t.Error("a failure the run introduced must not be excused as inherited")
	}
}

// A different failure in the same files is still this run's problem.
func TestBaselineTestFailureRepeated_aDifferentExceptionIsNotInherited(t *testing.T) {
	opts := EvalOptions{Lang: "csharp"}
	opts.BaselineTestSignature = TestFailureSignature(opts.Lang, baselineLog())

	other := strings.Replace(afterGenerationLog(),
		"System.ArgumentException : Format of the initialization string",
		"System.InvalidOperationException : No database provider has been configured", 1)

	if baselineTestFailureRepeated(opts, other) {
		t.Error("a different exception must not read as inherited")
	}
}

// Line numbers inside a diagnostic still normalise, so the fixer rewriting a file and shifting
// everything down does not make an inherited failure look new.
func TestTestFailureSignature_stillNormalisesLineNumbers(t *testing.T) {
	withLine := strings.Replace(inheritedBlocks, "GetKeyValuePair(String connectionString)",
		"GetKeyValuePair(String connectionString) in /workspace/src/Db.cs:line 12", 1)
	shifted := strings.Replace(withLine, ":line 12", ":line 87", 1)

	if TestFailureSignature("csharp", withLine) != TestFailureSignature("csharp", shifted) {
		t.Error("a source line shift changed the failure signature")
	}
}

// Output the extractor does not recognise must still be comparable: falling silent there would
// lose the one case the raw comparison did handle.
func TestTestFailureSignature_fallsBackToTheRawOutput(t *testing.T) {
	const opaque = "segfault\ncore dumped\n"
	if TestFailureSignature("csharp", opaque) == "" {
		t.Fatal("want a signature for unrecognised output, got none")
	}
	if TestFailureSignature("csharp", opaque) != TestFailureSignature("csharp", opaque) {
		t.Error("signature is not stable")
	}
	if TestFailureSignature("csharp", opaque) == TestFailureSignature("csharp", "different\n") {
		t.Error("unrelated opaque outputs share a signature")
	}
	if TestFailureSignature("csharp", "  \n ") != "" {
		t.Error("empty output must have no signature")
	}
}
