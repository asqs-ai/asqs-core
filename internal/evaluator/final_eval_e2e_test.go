package evaluator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingSandboxRunner captures the command the E2E pass hands to the runner.
type recordingSandboxRunner struct {
	stubSandboxRunner
	e2eCommands  []string
	unitCommands []string
}

func (r *recordingSandboxRunner) TestWithCommand(ctx context.Context, repoPath, lang, testCommand string) StepResult {
	r.unitCommands = append(r.unitCommands, testCommand)
	return r.test
}

func (r *recordingSandboxRunner) TestE2EPass(ctx context.Context, repoPath, lang, testCommand, e2eFramework string) StepResult {
	r.e2eCommands = append(r.e2eCommands, testCommand)
	return r.test
}

func greenRecorder() *recordingSandboxRunner {
	return &recordingSandboxRunner{stubSandboxRunner: stubSandboxRunner{
		compile:  StepResult{Step: StepCompile, OK: true},
		test:     StepResult{Step: StepTest, OK: true, Summary: "tests ok"},
		lint:     StepResult{Step: StepLint, OK: true},
		coverage: StepResult{Step: StepCoverage, OK: true},
	}}
}

func mavenRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Run api-5a67a414d4ba22496fcc23e1143076fa: no build commands configured, so the E2E pass fell
// back to the unit command and Surefire never saw the *E2EIT class. The pass must resolve the
// stack's own runner instead.
func TestRunRunFinalEval_e2ePassResolvesStackCommand(t *testing.T) {
	r := greenRecorder()
	res := RunRunFinalEval(context.Background(), r, EvalOptions{
		Lang: "java", RepoPath: mavenRepo(t), E2EFramework: "playwright-java", RunE2ETestPass: true,
	}, nil)
	if !res.Stable {
		t.Fatalf("expected stable, got %+v", res.StepResults)
	}
	if len(r.e2eCommands) != 1 || r.e2eCommands[0] != "mvn -q -B failsafe:integration-test failsafe:verify" {
		t.Fatalf("e2e commands = %q, want the Failsafe goals", r.e2eCommands)
	}
	var e2e *StepResult
	for i := range res.StepResults {
		if res.StepResults[i].Step == StepTestE2E {
			e2e = &res.StepResults[i]
		}
	}
	if e2e == nil {
		t.Fatalf("no %s step recorded: %+v", StepTestE2E, res.StepResults)
	}
	if !strings.HasPrefix(e2e.Summary, "e2e: ") {
		t.Fatalf("E2E summary %q is not labelled", e2e.Summary)
	}
}

func TestRunRunFinalEval_e2ePassHonoursConfiguredCommand(t *testing.T) {
	r := greenRecorder()
	RunRunFinalEval(context.Background(), r, EvalOptions{
		Lang: "typescript", RepoPath: t.TempDir(), E2EFramework: "playwright", RunE2ETestPass: true,
		UnitTestCommand: "npm run test:asqs", E2ETestCommand: "npx playwright test --project=api",
	}, nil)
	if len(r.e2eCommands) != 1 || r.e2eCommands[0] != "npx playwright test --project=api" {
		t.Fatalf("e2e commands = %q", r.e2eCommands)
	}
	if len(r.unitCommands) != 1 || r.unitCommands[0] != "npm run test:asqs" {
		t.Fatalf("unit commands = %q", r.unitCommands)
	}
}

// Running the unit suite a second time under the E2E label proves nothing; the pass is skipped and
// the run stays stable on the unit result.
func TestRunRunFinalEval_e2ePassSkippedWhenItWouldRepeatTheUnitCommand(t *testing.T) {
	r := greenRecorder()
	res := RunRunFinalEval(context.Background(), r, EvalOptions{
		Lang: "typescript", RepoPath: t.TempDir(), E2EFramework: "playwright", RunE2ETestPass: true,
		TestCommand: "npx playwright test", E2ETestCommand: "npx playwright test",
	}, nil)
	if !res.Stable {
		t.Fatalf("expected stable, got %+v", res.StepResults)
	}
	if len(r.e2eCommands) != 0 {
		t.Fatalf("E2E pass ran %q although it equals the unit command", r.e2eCommands)
	}
}

func TestRunRunFinalEval_e2ePassSkippedWhenNothingResolves(t *testing.T) {
	r := greenRecorder()
	res := RunRunFinalEval(context.Background(), r, EvalOptions{
		Lang: "python", RepoPath: t.TempDir(), E2EFramework: "playwright", RunE2ETestPass: true,
	}, nil)
	if !res.Stable {
		t.Fatalf("expected stable, got %+v", res.StepResults)
	}
	for _, sr := range res.StepResults {
		if sr.Step == StepTestE2E {
			t.Fatalf("an E2E step was recorded without a command: %+v", sr)
		}
	}
	if len(r.e2eCommands) != 0 {
		t.Fatalf("E2E pass ran %q", r.e2eCommands)
	}
}

func TestRunRunFinalEval_e2eFailureIsReportedAsE2E(t *testing.T) {
	// Unit passes, E2E fails: TestE2EPass returns its own verdict.
	e2eFail := &e2eFailingRunner{recordingSandboxRunner: greenRecorder()}
	res := RunRunFinalEval(context.Background(), e2eFail, EvalOptions{
		Lang: "java", RepoPath: mavenRepo(t), E2EFramework: "playwright-java", RunE2ETestPass: true,
	}, nil)
	if res.Stable {
		t.Fatal("expected an unstable result on an E2E failure")
	}
	if res.FailingStep != StepTestE2E {
		t.Fatalf("FailingStep = %q, want %q", res.FailingStep, StepTestE2E)
	}
}

type e2eFailingRunner struct{ *recordingSandboxRunner }

func (r *e2eFailingRunner) TestE2EPass(ctx context.Context, repoPath, lang, testCommand, e2eFramework string) StepResult {
	r.e2eCommands = append(r.e2eCommands, testCommand)
	return StepResult{Step: StepTest, OK: false, Summary: "1 failed", Output: "[ERROR] Tests run: 1, Failures: 1"}
}
