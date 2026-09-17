package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSandboxWithTest answers for both steps so a baseline can cover compile AND test.
type stubSandboxWithTest struct {
	compile      StepResult
	test         StepResult
	compileCalls int
	testCalls    int
}

func (s *stubSandboxWithTest) Compile(context.Context, string, string) StepResult {
	s.compileCalls++
	return s.compile
}
func (s *stubSandboxWithTest) Test(context.Context, string, string) StepResult {
	s.testCalls++
	return s.test
}
func (s *stubSandboxWithTest) Lint(context.Context, string, string) StepResult {
	return StepResult{OK: true}
}
func (s *stubSandboxWithTest) Coverage(context.Context, string, string) StepResult {
	return StepResult{OK: true}
}
func (s *stubSandboxWithTest) Mutation(context.Context, string, string, []string) StepResult {
	return StepResult{OK: true}
}

// A tree that COMPILES before the run can still be red, and the baseline only ever compiled — so
// every pre-existing test failure was attributed to the run that happened to be standing there.
//
// A validation run: four failing tests, all of them the repository's own and
// all of them environmental — one asserts Docker is running (it is not, inside the eval container)
// and three go through a SQLite fallback that only runs when Docker is absent. The run generated
// ten tests, none of which failed, spent 34 minutes in the fix loop and reported unstable.
func TestCaptureBaselineFailures_capturesFailingTestsOnACompilingTree(t *testing.T) {
	repo := t.TempDir()
	writeBaselineFile(t, repo, "tests/App.FunctionalTests/DockerAvailabilityTests.cs", "public class DockerAvailabilityTests {}\n")
	writeBaselineFile(t, repo, "tests/App.FunctionalTests/ApiEndpoints/ContributorList.cs", "public class ContributorList {}\n")

	testOut := "[xUnit.net] DockerAvailabilityTests.Docker_ShouldBeRunning [FAIL]\n" +
		"    /workspace/tests/App.FunctionalTests/DockerAvailabilityTests.cs(19,0): at DockerAvailabilityTests.Docker_ShouldBeRunning()\n" +
		"    /workspace/tests/App.FunctionalTests/ApiEndpoints/ContributorList.cs(14,0): at ContributorList.ReturnsTwoContributors()\n"
	sb := &stubSandboxWithTest{
		compile: StepResult{Step: StepCompile, OK: true},
		test:    StepResult{Step: StepTest, OK: false, Output: testOut},
	}
	base := CaptureBaselineFailures(context.Background(), sb, EvalOptions{RepoPath: repo, Lang: "csharp"})

	if !base.Captured {
		t.Fatal("expected a captured baseline")
	}
	if !base.Clean {
		t.Error("the tree compiled; Clean describes the compile step and must stay true")
	}
	if !base.TestsCaptured || base.TestsClean {
		t.Fatalf("expected a captured, failing test baseline: %+v", base)
	}
	if sb.testCalls != 1 {
		t.Errorf("test step ran %d time(s); want exactly one baseline run", sb.testCalls)
	}
	for _, p := range []string{
		"tests/App.FunctionalTests/DockerAvailabilityTests.cs",
		"tests/App.FunctionalTests/ApiEndpoints/ContributorList.cs",
	} {
		if !base.Inherited(p) {
			t.Errorf("%s was failing before the run and must count as inherited (paths=%v)", p, base.Paths)
		}
	}

	// The same failure at the end of the run is inherited, not introduced — which is the difference
	// between "this run broke the tree" and "this run left it as it found it".
	inherited, introduced := ClassifyFailures(base, testOut, repo)
	if len(inherited) != 2 || len(introduced) != 0 {
		t.Fatalf("inherited=%v introduced=%v; want both failures inherited", inherited, introduced)
	}
	if d := EvaluateBaselineProgress(base, testOut, repo).Describe(); !strings.Contains(d, "inherited") {
		t.Errorf("Describe() = %q; want it to report the inherited set", d)
	}
}

// A tree that does not compile cannot be tested, so the test baseline is not attempted and says so
// rather than reporting a clean one.
func TestCaptureBaselineFailures_skipsTestsWhenTheBaselineDoesNotCompile(t *testing.T) {
	repo := t.TempDir()
	sb := &stubSandboxWithTest{
		compile: StepResult{Step: StepCompile, OK: false, Output: "error CS1002: ; expected\n"},
		test:    StepResult{Step: StepTest, OK: true},
	}
	base := CaptureBaselineFailures(context.Background(), sb, EvalOptions{RepoPath: repo, Lang: "csharp"})
	if base.TestsCaptured {
		t.Error("tests cannot run on a tree that does not compile; the baseline must not claim to know")
	}
	if sb.testCalls != 0 {
		t.Errorf("test step ran %d time(s) on a non-compiling tree", sb.testCalls)
	}
}

func writeBaselineFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
