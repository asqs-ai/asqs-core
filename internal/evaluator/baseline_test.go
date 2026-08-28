package evaluator

import (
	"context"
	"strings"
	"testing"
)

// ownerPkg is the fixture package path these tests write into (upstream keeps this constant in a
// sibling test file this port does not carry).
const ownerPkg = "src/test/java/org/springframework/samples/petclinic/owner/"

// baselineStubSandbox returns a fixed compile result.
type baselineStubSandbox struct {
	compile StepResult
	calls   int
}

func (s *baselineStubSandbox) Compile(_ context.Context, _, _ string) StepResult {
	s.calls++
	return s.compile
}
func (s *baselineStubSandbox) Test(context.Context, string, string) StepResult {
	return StepResult{OK: true}
}
func (s *baselineStubSandbox) Lint(context.Context, string, string) StepResult {
	return StepResult{OK: true}
}
func (s *baselineStubSandbox) Coverage(context.Context, string, string) StepResult {
	return StepResult{OK: true}
}
func (s *baselineStubSandbox) Mutation(context.Context, string, string, []string) StepResult {
	return StepResult{OK: true}
}

// The exact shape of the motivating run: the tree did not compile before generation, and the files
// that stalled the fix loop came from a prior run's commit.
func TestCaptureBaselineFailures_recordsPreExistingBreakage(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, ownerPkg+"OwnerTest.java", "package p;\nclass OwnerTest {}\n")
	writeRepoFile(t, repo, "src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTest.java",
		"package p;\nclass PetClinicRuntimeHintsTests {}\n")

	out := "[ERROR] COMPILATION ERROR :\n" +
		"[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTest.java:[17,17] cannot find symbol\n" +
		"[ERROR] /workspace/" + ownerPkg + "OwnerTest.java:[119,17] reference to assertThat is ambiguous\n"
	sb := &baselineStubSandbox{compile: StepResult{Step: StepCompile, OK: false, Output: out}}
	opts := EvalOptions{RepoPath: repo, Lang: "java"}

	base := CaptureBaselineFailures(context.Background(), sb, opts)

	if !base.Captured || base.Clean {
		t.Fatalf("expected a captured, non-clean baseline: %+v", base)
	}
	if len(base.Paths) != 2 {
		t.Errorf("baseline paths = %v, want both failing files", base.Paths)
	}
	if !base.Inherited(ownerPkg + "OwnerTest.java") {
		t.Error("OwnerTest.java must be recognised as inherited")
	}
	if base.Inherited(ownerPkg + "SomethingElse.java") {
		t.Error("an unrelated file must not be inherited")
	}
	if base.Signature == "" {
		t.Error("a failing baseline must carry a signature")
	}
	if sb.calls != 1 {
		t.Errorf("baseline compiled %d times; must be exactly one invocation per run", sb.calls)
	}
}

func TestCaptureBaselineFailures_cleanTree(t *testing.T) {
	repo := t.TempDir()
	sb := &baselineStubSandbox{compile: StepResult{Step: StepCompile, OK: true}}
	opts := EvalOptions{RepoPath: repo, Lang: "java"}

	base := CaptureBaselineFailures(context.Background(), sb, opts)
	if !base.Captured || !base.Clean || len(base.Paths) != 0 {
		t.Errorf("clean baseline = %+v", base)
	}
	// A clean baseline means every later failure is this run's doing.
	writeRepoFile(t, repo, "a/BTest.java", "class B {}\n")
	_, introduced := ClassifyFailures(base, "[ERROR] /workspace/a/BTest.java:[1,1] boom\n", repo)
	if len(introduced) != 1 {
		t.Errorf("introduced = %v, want 1", introduced)
	}
}

// Not capturing a baseline must never be read as "the tree was clean" — that would misattribute
// every inherited failure to the run.
func TestCaptureBaselineFailures_notCapturedIsNotClean(t *testing.T) {
	base := CaptureBaselineFailures(context.Background(), nil, EvalOptions{})
	if base.Captured {
		t.Fatal("no sandbox must yield an uncaptured baseline")
	}
	if base.Inherited("anything.java") {
		t.Error("an uncaptured baseline must not claim anything is inherited")
	}
	repo := t.TempDir()
	writeRepoFile(t, repo, "a/BTest.java", "class B {}\n")
	inherited, introduced := ClassifyFailures(base, "[ERROR] /workspace/a/BTest.java:[1,1] boom\n", repo)
	if len(inherited) != 0 {
		t.Errorf("uncaptured baseline claimed inherited failures: %v", inherited)
	}
	if len(introduced) != 1 {
		t.Errorf("introduced = %v, want the failure attributed to the run", introduced)
	}
}

func TestClassifyFailures_splitsInheritedFromIntroduced(t *testing.T) {
	// AllCitedRepoPaths resolves a diagnostic path against the repo on disk — the same extractor
	// the fixer uses, so classification and adoption can never disagree about what a path means.
	repo := t.TempDir()
	writeRepoFile(t, repo, "a/OldTest.java", "class O {}\n")
	writeRepoFile(t, repo, "a/NewTest.java", "class N {}\n")
	base := BaselineFailures{Captured: true, Paths: []string{"a/OldTest.java"}}
	inherited, introduced := ClassifyFailures(base,
		"[ERROR] /workspace/a/OldTest.java:[1,1] boom\n[ERROR] /workspace/a/NewTest.java:[2,2] bang\n", repo)
	if len(inherited) != 1 || inherited[0] != "a/OldTest.java" {
		t.Errorf("inherited = %v", inherited)
	}
	if len(introduced) != 1 || introduced[0] != "a/NewTest.java" {
		t.Errorf("introduced = %v", introduced)
	}
}

func TestBaselineProgress(t *testing.T) {
	cases := []struct {
		name              string
		p                 BaselineProgress
		improved          bool
		describeSubstring string
	}{
		{"repaired some", BaselineProgress{Known: true, BaselineCount: 5, StillFailing: 2}, true, "2 of 5"},
		{"repaired none", BaselineProgress{Known: true, BaselineCount: 5, StillFailing: 5}, false, "5 of 5"},
		{"clean and stayed clean", BaselineProgress{Known: true}, false, "compiled before the run and still does"},
		{"clean then broke it", BaselineProgress{Known: true, Introduced: 3}, false, "introduced 3"},
		{"unknown", BaselineProgress{}, false, "no baseline was captured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Improved(); got != tc.improved {
				t.Errorf("Improved() = %v, want %v", got, tc.improved)
			}
			if !strings.Contains(tc.p.Describe(), tc.describeSubstring) {
				t.Errorf("Describe() = %q, want it to contain %q", tc.p.Describe(), tc.describeSubstring)
			}
		})
	}
}

// Upstream carries two more tests here that this port does not: the rerun progress gate and the
// ProductionToolState baseline summary. Both live on the session engine's rerun scheduling and
// its tool-state object, which are outside core's seam — core's pipeline runs once and schedules
// nothing — so there is no landing site for either. EvaluateBaselineProgress itself IS ported
// above, because the measurement is evaluator-domain and feeds the audit.
