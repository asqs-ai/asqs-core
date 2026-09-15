package evaluator

import (
	"strings"
	"testing"
)

// A 1738-line test log trimmed to its first three lines is the framework's banner and nothing else.
// Run api-8d5367b3383017e25f09e53dacf6c275 recorded a 348-character baseline summary that named no
// failure at all, while listing nine failing paths — so an operator could see WHICH files were
// already red and never WHY, and neither could anything downstream.
func TestBaselineTestSummary_keepsTheFailureNotTheBanner(t *testing.T) {
	log := strings.Join([]string{
		"Test run for /workspace/tests/App.IntegrationTests/bin/Release/net10.0/App.IntegrationTests.dll (.NETCoreApp,Version=v10.0)",
		"A total of 1 test files matched the specified pattern.",
		"Test run for /workspace/tests/App.UnitTests/bin/Release/net10.0/App.UnitTests.dll (.NETCoreApp,Version=v10.0)",
		"  Determining projects to restore...",
		"[xUnit.net 00:00:00.93]     App.FunctionalTests.ApiEndpoints.ContributorList.ReturnsTwo [FAIL]",
		"[xUnit.net 00:00:00.93]       System.ArgumentException : Cannot create an instance of App.FunctionalTests.CustomWebApplicationFactory`1[TProgram] because Type.ContainsGenericParameters is true.",
		"[xUnit.net 00:00:00.93]       Stack Trace:",
		"[xUnit.net 00:00:00.93]         at App.FunctionalTests.ApiEndpoints.ContributorList..ctor()",
		"Failed!  - Failed: 3, Passed: 12, Skipped: 0, Total: 15",
	}, "\n")

	got := baselineTestSummary(log)
	if got == "" {
		t.Fatal("baseline summary is empty")
	}
	if !strings.Contains(got, "ContainsGenericParameters") {
		t.Errorf("the summary does not carry the failure that caused it:\n%s", got)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "Test run for ") && !strings.Contains(got, "FAIL") {
		t.Errorf("the summary is the framework banner again:\n%s", got)
	}
}

// An output with nothing recognisable in it must still produce something rather than an empty
// string: a baseline that failed and reports no reason is worse than a truncated one.
func TestBaselineTestSummary_fallsBackWhenNothingIsRecognisable(t *testing.T) {
	if got := baselineTestSummary("segfault\ncore dumped\n"); strings.TrimSpace(got) == "" {
		t.Error("want some excerpt for an unrecognisable failure, got empty")
	}
	if got := baselineTestSummary("   \n  \n"); strings.TrimSpace(got) != "" {
		t.Errorf("want empty for empty input, got %q", got)
	}
}
