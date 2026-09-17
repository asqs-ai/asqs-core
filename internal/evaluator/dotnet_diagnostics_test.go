package evaluator

import (
	"os"
	"path/filepath"
	"testing"
)

// resolutionFailureRE decides whether a fixer round that only changed imports made progress. It
// carried four C# phrases and not the two most common: CS1061's "does not contain a definition for"
// (a missing using for an extension method, repaired by an import) and CS0117. Those rounds read as
// "no progress" and the loop reverted a correct repair.
func TestResolutionFailureRE_csharpMemberDiagnostics(t *testing.T) {
	match := []string{
		`/workspace/Broken.cs(7,11): error CS1061: 'OrderService' does not contain a definition for 'NoSuchMethod' and no accessible extension method 'NoSuchMethod' accepting a first argument of type 'OrderService' could be found (are you missing a using directive or an assembly reference?)`,
		`/workspace/T.cs(3,9): error CS0117: 'Order' does not contain a definition for 'Sku'`,
		`/workspace/T.cs(4,9): error CS0103: The name 'Assert' does not exist in the current context`,
		`/workspace/T.cs(1,7): error CS0246: The type or namespace name 'Xunit' could not be found`,
	}
	for _, line := range match {
		if !resolutionFailureRE.MatchString(line) {
			t.Errorf("not classed as a resolution failure:\n  %s", line)
		}
	}
	// A genuine semantic error is not an import problem, and a round that leaves it alone has made
	// no progress — which is what this class exists to distinguish.
	noMatch := []string{
		`/workspace/T.cs(8,17): error CS1503: Argument 1: cannot convert from 'string' to 'int'`,
		`/workspace/T.cs(9,5): error CS0165: Use of unassigned local variable 'x'`,
	}
	for _, line := range noMatch {
		if resolutionFailureRE.MatchString(line) {
			t.Errorf("a semantic error was classed as a resolution failure:\n  %s", line)
		}
	}
}

// `dotnet test` exits non-zero when it discovers nothing, with a message that names no test and no
// file. That is not a failure the fix loop can repair: the JS path has recognised its equivalent
// since the vitest work, and the C# dialect was never added.
func TestDotnetNoTestsRan(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "dotnet_output", "vstest_no_tests.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !dotnetNoTestsRan(string(b)) {
		t.Errorf("a real 'No test is available' run was not recognised:\n%s", b)
	}

	for _, s := range []string{
		"Total tests: 0\n     Passed: 0\n",
		"No test is available in /workspace/bin/Release/net8.0/App.Tests.dll.",
	} {
		if !dotnetNoTestsRan(s) {
			t.Errorf("not recognised as an empty run:\n%s", s)
		}
	}
	for _, s := range []string{
		"Total tests: 3\n     Failed: 3\n",
		"Passed!  - Failed: 0, Passed: 3, Skipped: 0, Total: 3",
		"",
	} {
		if dotnetNoTestsRan(s) {
			t.Errorf("a run that executed tests was treated as empty:\n%s", s)
		}
	}
}
