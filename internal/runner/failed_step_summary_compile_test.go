package runner

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// A failed compile is summarised from the first lines of the build log, and MSBuild opens with the
// projects that SUCCEEDED. A validation run therefore recorded eight
// identical rows reading
//
//	compile step failed: Clean.Architecture.Core -> /workspace/.../Clean.Architecture.Core.dll
//
// which is a success line. The audit is the only post-mortem artifact there is, and for eight
// rounds of a failing compile it said nothing about what was wrong.
func TestFailedStepSummary_compileNamesTheDiagnosticNotTheProjectsThatBuilt(t *testing.T) {
	out := strings.Join([]string{
		"  Determining projects to restore...",
		"  Clean.Architecture.Core -> /workspace/src/Clean.Architecture.Core/bin/Release/net10.0/Clean.Architecture.Core.dll",
		"  Clean.Architecture.ServiceDefaults -> /workspace/src/Clean.Architecture.ServiceDefaults/bin/Release/net10.0/x.dll",
		"  Clean.Architecture.UseCases -> /workspace/src/Clean.Architecture.UseCases/bin/Release/net10.0/x.dll",
		"  Clean.Architecture.Infrastructure -> /workspace/src/Clean.Architecture.Infrastructure/bin/Release/net10.0/x.dll",
		"  Clean.Architecture.UnitTests -> /workspace/tests/Clean.Architecture.UnitTests/bin/Release/net10.0/x.dll",
		"/workspace/tests/Clean.Architecture.FunctionalTests/Projects/AddToDoItem/AddToDoItemHandlerTests.cs(4,7): error CS0246: The type or namespace name 'NimblePros' could not be found (are you missing a using directive or an assembly reference?) [/workspace/tests/Clean.Architecture.FunctionalTests/Clean.Architecture.FunctionalTests.csproj]",
		"/workspace/tests/Clean.Architecture.FunctionalTests/SeedDataTests.cs(9,20): error CS0234: The type or namespace name 'SampleToDo' does not exist in the namespace 'NimblePros'",
		"",
		"Build FAILED.",
		"    0 Warning(s)",
		"    2 Error(s)",
	}, "\n")

	got := failedStepSummary(evaluator.StepCompile, out, 5)
	if !strings.Contains(got, "error CS0246") {
		t.Fatalf("summary does not name the compiler error:\n%s", got)
	}
	if strings.Contains(got, "Determining projects to restore") {
		t.Errorf("summary leads with restore noise instead of the diagnostic:\n%s", got)
	}
	if strings.Contains(got, "Clean.Architecture.Core ->") {
		t.Errorf("summary reports a project that built successfully:\n%s", got)
	}
}

// Java keeps working the way it did: its diagnostics already appear at the top of the log, and the
// extractor must not throw them away.
func TestFailedStepSummary_compileKeepsJavaDiagnostics(t *testing.T) {
	out := strings.Join([]string{
		"[ERROR] COMPILATION ERROR :",
		"[ERROR] /workspace/src/test/java/p/FooTest.java:[19,17] cannot find symbol",
		"[INFO] BUILD FAILURE",
	}, "\n")
	got := failedStepSummary(evaluator.StepCompile, out, 5)
	if !strings.Contains(got, "cannot find symbol") {
		t.Fatalf("summary lost the javac diagnostic:\n%s", got)
	}
}

// Output with no diagnostic in it at all still summarises to something, as before: a build killed
// by the host, a wrapper with no execute bit.
func TestFailedStepSummary_compileFallsBackToTheHeadWhenNothingLooksLikeADiagnostic(t *testing.T) {
	out := "fork/exec ./mvnw: permission denied\n"
	if got := failedStepSummary(evaluator.StepCompile, out, 5); !strings.Contains(got, "permission denied") {
		t.Fatalf("summary = %q; want the only line there is", got)
	}
}
