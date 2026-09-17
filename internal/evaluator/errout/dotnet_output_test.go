package errout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func realOutput(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "dotnet_output", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// A .NET frame is a FRAME, not a continuation line. testFailureStackFrameLine listed .java, .kt,
// .ts, .tsx and .js — so every C# frame was counted against the per-block continuation budget
// (20 lines of assertion diff) instead of the frame budget (8), and a failure whose stack was long
// enough lost the frames that name the repo's own files.
func TestTestFailureBlocks_dotnetFrames(t *testing.T) {
	out := realOutput(t, "vstest_failures.txt")
	got := ExtractTestFailureBlocks(out)
	if strings.TrimSpace(got) == "" {
		t.Fatal("no failure block extracted from a real dotnet test failure")
	}
	for _, want := range []string{
		"OrderService.Total",
		"T.cs:line 10",
		"Assert.Equal() Failure",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure block is missing %q:\n%s", want, got)
		}
	}
	// The framework's own reflection frames are noise the fixer cannot act on.
	if strings.Count(got, "MethodBaseInvoker") > 2 {
		t.Errorf("failure block is dominated by framework frames:\n%s", got)
	}
}

// MSBuild prints every diagnostic TWICE — once inline, once in the recap after `Build FAILED.` —
// then a `N Warning(s) / M Error(s)` footer and `Time Elapsed`. Sanitize returned the raw text for
// every language but Java, so a C# compile failure spent half the fixer's prompt budget on a
// verbatim copy of its own first half.
func TestSanitize_msbuild(t *testing.T) {
	raw := realOutput(t, "msbuild_failed.txt")
	got := Sanitize("csharp", raw)

	if strings.Count(raw, "error CS1061") < 2 {
		t.Fatalf("fixture no longer contains the duplicated recap; this test needs rewriting:\n%s", raw)
	}
	if n := strings.Count(got, "error CS1061"); n != 1 {
		t.Errorf("CS1061 appears %d times after sanitising, want exactly 1:\n%s", n, got)
	}
	if !strings.Contains(got, "does not contain a definition for 'NoSuchMethod'") {
		t.Errorf("the diagnostic text itself was dropped:\n%s", got)
	}
	if !strings.Contains(got, "error CS1503") {
		t.Errorf("a second distinct diagnostic was dropped:\n%s", got)
	}
	for _, noise := range []string{"Time Elapsed", "Warning(s)", "Error(s)"} {
		if strings.Contains(got, noise) {
			t.Errorf("%q survived sanitising:\n%s", noise, got)
		}
	}
	if len(got) >= len(raw) {
		t.Errorf("sanitising did not shrink the output (%d -> %d)", len(raw), len(got))
	}
}

// Idempotent, like the Maven path: the fix loop sanitises the same output more than once.
func TestSanitize_msbuildIsIdempotent(t *testing.T) {
	raw := realOutput(t, "msbuild_failed.txt")
	once := Sanitize("csharp", raw)
	if twice := Sanitize("csharp", once); twice != once {
		t.Errorf("Sanitize is not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// The `cs` alias takes the same path, and other languages are untouched.
func TestSanitize_languageRouting(t *testing.T) {
	raw := realOutput(t, "msbuild_failed.txt")
	if Sanitize("cs", raw) != Sanitize("csharp", raw) {
		t.Error(`Sanitize("cs") and Sanitize("csharp") disagree`)
	}
	if Sanitize("typescript", raw) != raw {
		t.Error("a TypeScript log was rewritten by the MSBuild sanitiser")
	}
}

// A build that succeeds carries no recap to cut, and must come back intact.
func TestSanitize_msbuildSuccessUntouched(t *testing.T) {
	const ok = `  Determining projects to restore...
  All projects are up-to-date for restore.
  T -> /workspace/bin/Release/net10.0/T.dll

Build succeeded.
    0 Warning(s)
    0 Error(s)

Time Elapsed 00:00:02.06
`
	if got := Sanitize("csharp", ok); !strings.Contains(got, "Build succeeded.") {
		t.Errorf("a successful build lost its verdict:\n%s", got)
	}
}

// A .NET frame must be classified as a FRAME, not as a continuation line. Both end up in the block,
// so a content assertion cannot tell them apart — but they are drawn from different budgets
// (maxFramesPerFailureBlock = 8 versus maxContinuationLinesPerBlock = 20), and a C# stack counted
// as prose crowds out the assertion diff it was supposed to leave room for.
func TestTestFailureStackFrameLine_dotnet(t *testing.T) {
	frames := []string{
		`at Asqs.Probe.OrderService.Total(Int32 qty, Decimal unit) in /workspace/src/Core/OrderService.cs:line 10`,
		`at Shop.Components.Counter.Increment() in /workspace/src/Components/Counter.razor:line 14`,
		`at System.Reflection.MethodBaseInvoker.InterpretedInvoke_Method(Object obj, IntPtr* args)`,
	}
	for _, f := range frames {
		if !testFailureStackFrameLine(f) {
			t.Errorf("not classified as a stack frame:\n  %s", f)
		}
	}
	notFrames := []string{
		`Assert.Equal() Failure: Values differ`,
		`Expected: 20`,
		`  Error Message:`,
	}
	for _, f := range notFrames {
		if testFailureStackFrameLine(f) {
			t.Errorf("assertion prose classified as a stack frame:\n  %s", f)
		}
	}
}
