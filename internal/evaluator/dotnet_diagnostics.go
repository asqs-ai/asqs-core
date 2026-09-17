package evaluator

import (
	"regexp"
	"strings"
)

// dotnetNoTestsRan reports whether a `dotnet test` run discovered nothing.
//
// VSTest exits non-zero when it finds no tests, with a message that names no test and no file:
//
//	No test is available in /workspace/bin/Release/net8.0/App.Tests.dll. Make sure that test
//	discoverer & executors are registered and platform & framework version settings are appropriate
//	and try again.
//
// That is not a failure the fix loop can repair — there is nothing to repair — and feeding it to the
// fixer spends rounds rewriting tests that were never executed. The JS path has recognised its
// vitest/jest equivalent since 1b6ffea; the C# dialect was never added, so a C# run in that state
// burned its whole budget.
// DotnetNoTestsRan is the exported form for internal/runner, which decides at the step boundary
// whether a non-zero exit was an empty run.
func DotnetNoTestsRan(output string) bool { return dotnetNoTestsRan(output) }

func dotnetNoTestsRan(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	return reDotnetNoTestAvailable.MatchString(output) || reDotnetZeroTotalTests.MatchString(output)
}

var (
	reDotnetNoTestAvailable = regexp.MustCompile(`(?i)No test is available in\b`)
	// `Total tests: 0` is the other shape, from a run that discovered an assembly but no test in it.
	// Anchored on zero specifically: `Total tests: 3` is a run that did work.
	reDotnetZeroTotalTests = regexp.MustCompile(`(?im)^\s*Total tests:\s*0\s*$`)
)
