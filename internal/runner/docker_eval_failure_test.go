package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/errclass"
)

// fakeDockerSandbox returns a docker-type Sandbox whose "docker binary" is the given shell script,
// plus a repo that resolves to the java-maven toolchain profile.
//
// `exec` in the script matters where the process must be killed: it replaces the shell so the job
// has a single child and CombinedOutput cannot block on a surviving grandchild.
func fakeDockerSandbox(t *testing.T, timeout, script string) (*Sandbox, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Sandbox{Type: "docker", Timeout: timeout, DockerBinary: bin}, repo
}

// The bug this bundle fixes: a container killed at general.sandbox.timeout used to be reported as
// {OK: false, Summary: "failed", Output: ""} — no mention of the deadline, and an empty Output
// that sends the fixer off to repair code that was fine.
func TestDockerTest_timeoutIsNamedInTheSummary(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "150ms", "exec sleep 30")

	res := sb.Test(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("a timed-out step must not report OK")
	}
	if !strings.Contains(res.Summary, "step timed out after 150ms (general.sandbox.timeout)") {
		t.Fatalf("summary does not name the timeout: %q", res.Summary)
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Error("Output must not be empty: empty output is treated as in-scope by the evaluator, " +
			"which is what handed the fixer a blank prompt")
	}
	if res.Summary == "failed" {
		t.Error("regressed to the bare pre-fix summary")
	}
	if res.Step != evaluator.StepTest {
		t.Errorf("Step = %v, want %v", res.Step, evaluator.StepTest)
	}
}

// The summary must be classifiable, otherwise skip_fixer_on_infrastructure_failure and the
// compile-abort path still cannot tell a killed container from a real test failure.
func TestDockerTest_timeoutIsClassifiedAsStepTimeout(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "150ms", "exec sleep 30")

	res := sb.Test(context.Background(), repo, "java")

	if got := errclass.Kind("java", res.Summary); got != errclass.KindStepTimeout {
		t.Fatalf("errclass.Kind(summary) = %q, want %q\nsummary: %q", got, errclass.KindStepTimeout, res.Summary)
	}
	if got := errclass.Kind("java", res.Output); got != errclass.KindStepTimeout {
		t.Errorf("errclass.Kind(output) = %q, want %q", got, errclass.KindStepTimeout)
	}
}

func TestDockerCompile_timeoutIsNamedInTheSummary(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "150ms", "exec sleep 30")

	res := sb.Compile(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("a timed-out compile must not report OK")
	}
	if !strings.Contains(res.Summary, "compile step timed out after 150ms (general.sandbox.timeout)") {
		t.Fatalf("summary does not name the timeout: %q", res.Summary)
	}
}

// Regression guard: an ordinary non-zero container exit is a build failure, not a job failure. It
// must keep reporting the container's own output and must NOT be dressed up as a timeout.
func TestDockerTest_nonZeroExitStillReportsContainerOutput(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", `echo "COMPILATION ERROR: cannot find symbol"
exit 1`)

	res := sb.Test(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("non-zero exit must fail the step")
	}
	if !strings.Contains(res.Output, "cannot find symbol") {
		t.Errorf("container output lost: %q", res.Output)
	}
	if strings.Contains(res.Summary, "timed out") {
		t.Errorf("ordinary failure mislabelled as a timeout: %q", res.Summary)
	}
	if got := errclass.Kind("java", res.Summary); got != "" {
		t.Errorf("ordinary build failure must not classify as an execution kind, got %q", got)
	}
}

// Regression guard for the case the OLD gate did handle: the docker CLI never starts, so there is
// no ProcessState, ExitCode stays 0, and the run error is the only diagnostic. It must still reach
// both Summary and Output.
func TestDockerTest_missingDockerBinaryReportsTheExecError(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", "exec sleep 30")
	sb.DockerBinary = filepath.Join(t.TempDir(), "docker-does-not-exist")

	res := sb.Test(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("a docker binary that cannot be executed must fail the step")
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Error("Output must carry the exec error when nothing else exists")
	}
	if !strings.Contains(res.Summary, "docker") {
		t.Errorf("summary should name the failing binary: %q", res.Summary)
	}
	if strings.Contains(res.Summary, "timed out") {
		t.Errorf("a missing binary is not a timeout: %q", res.Summary)
	}
}

// A successful container run must stay successful — the new gate keys on runErr, and jobrunner
// returns a nil error for exit 0.
func TestDockerTest_successIsUnaffected(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", "exit 0")

	res := sb.Test(context.Background(), repo, "java")

	if !res.OK {
		t.Fatalf("exit 0 must report OK, got summary %q", res.Summary)
	}
}

// The sandbox sets CI=true, which makes vitest colour its output even into a pipe; every parser
// downstream (excerpt, discard attribution, scope narrowing) expects plain text. The strip happens
// once, at capture, so a coloured runner cannot reach any of them.
func TestDockerTest_outputIsStrippedOfANSI(t *testing.T) {
	sb, repo := fakeDockerSandbox(t, "30s", `printf ' \033[32m✓\033[39m src/app/AppLayout.test.tsx\n\033[41m\033[1m FAIL \033[22m\033[49m src/app/router.test.tsx:\033[2m59:24\033[22m\n'
exit 1`)

	// The fixture repo resolves to the java-maven profile; the strip is language-independent and a
	// "typescript" step on a pom.xml repo would be skipped before any capture happened.
	res := sb.Test(context.Background(), repo, "java")

	if res.OK {
		t.Fatal("non-zero exit must fail the step")
	}
	if strings.Contains(res.Output, "\x1b[") {
		t.Errorf("escape codes survived capture: %q", res.Output)
	}
	for _, want := range []string{"✓ src/app/AppLayout.test.tsx", "FAIL  src/app/router.test.tsx:59:24"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("stripped output lost %q: %q", want, res.Output)
		}
	}
}

// fakeDockerSandboxJS is fakeDockerSandbox for a Node package: the docker step plan for JS/TS
// resolves through package.json, and the runner's JS exit-code rules only apply to JS languages.
func fakeDockerSandboxJS(t *testing.T, script string) (*Sandbox, string) {
	t.Helper()
	sb, repo := fakeDockerSandbox(t, "30s", script)
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"x","scripts":{"test":"vitest run"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return sb, repo
}

// vitest exits 1 when its include pattern matches nothing. That is not a failed test: the step
// passes and carries evaluator.NoTestFilesSuffix so the evaluator can decide whether an empty tree
// is acceptable (only E2E specs left) or a misconfiguration (generated unit tests never seen).
func TestDockerTest_noTestFilesIsOkWithSuffix(t *testing.T) {
	sb, repo := fakeDockerSandboxJS(t, `case "$*" in
  *"npm test"*|*vitest*) printf ' RUN  v4.1.11 /workspace\n\nNo test files found, exiting with code 1\n\ninclude: **/*.{test,spec}.{js,ts,tsx}\nexclude:  **/node_modules/**, e2e/**\n'; exit 1;;
  *) exit 0;;
esac`)

	res := sb.Test(context.Background(), repo, "typescript")

	if !res.OK {
		t.Fatalf("an empty vitest run must not fail the step at the runner: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, evaluator.NoTestFilesSuffix) {
		t.Errorf("summary must carry NoTestFilesSuffix for the evaluator; got %q", res.Summary)
	}
	if strings.Contains(res.Summary, jsExitCodeIgnoredSuffix) {
		t.Errorf("no-tests must not be reported as the zero-failures case: %q", res.Summary)
	}
	if !strings.Contains(res.Output, "No test files found") {
		t.Errorf("runner output must be preserved: %q", res.Output)
	}
}
