package runner

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

func captureRunnerStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	b, _ := io.ReadAll(r)
	return string(b)
}

func javaRepoFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(jacocoPom), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// One env block for both targets (U8). Every fact in the shared core comes from the StepPlan, so
// the block cannot describe something other than what the executor runs.
func TestLogEvalEnvOnce_sharedCoreOnBothTargets(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	dir := javaRepoFixture(t)
	sb := &Sandbox{Type: "local", Timeout: "30m", BuildTool: "mvn"}
	plan, err := sb.buildStepPlan(dir, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	out := captureRunnerStderr(t, func() { sb.logEvalEnvOnce(plan, dir) })

	for _, want := range []string{
		"[asqs-eval] evaluation runner: type=local",
		"lang=java",
		`build_tool="mvn"`,
		"step_timeout=30m0s",
		"workdir: " + dir,
		"command_overrides:",
		"effective_argv:",
		"mvn -q -B -DskipTests test-compile",
		"mvn -q -B test jacoco:report",
		"host toolchain:",
		"CI=true",
		"toolchain=java-maven",
		"restore=[mvn -q -B dependency:go-offline]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("env block missing %q\n--- got ---\n%s", want, out)
		}
	}

	// The same core, from the same fields, on the docker target.
	dsb := &Sandbox{Type: "docker", Timeout: "30m", BuildTool: "mvn"}
	dplan, err := dsb.buildStepPlan(dir, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	dout := captureRunnerStderr(t, func() { dsb.logEvalEnvOnce(dplan, dir) })
	for _, want := range []string{
		"[asqs-eval] evaluation runner: type=docker",
		"lang=java", "toolchain=java-maven", "effective_argv:", "CI=true",
		"image=", "networks:", "workspace_mount:", "dependency_caches:",
	} {
		if !strings.Contains(dout, want) {
			t.Errorf("docker env block missing %q\n--- got ---\n%s", want, dout)
		}
	}
	// Facts that exist only on one target must stay on that target.
	if strings.Contains(dout, "host toolchain:") {
		t.Error("the docker block must not claim a host toolchain")
	}
	if strings.Contains(out, "workspace_mount:") {
		t.Error("the local block must not claim a container mount")
	}
}

// Once per Sandbox, on either target.
func TestLogEvalEnvOnce_OnlyOnce(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	dir := javaRepoFixture(t)
	sb := &Sandbox{Type: "local", Timeout: "5m", BuildTool: "mvn"}
	plan, err := sb.buildStepPlan(dir, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	out := captureRunnerStderr(t, func() {
		sb.logEvalEnvOnce(plan, dir)
		sb.logEvalEnvOnce(plan, dir)
	})
	if n := strings.Count(out, "evaluation runner: type=local"); n != 1 {
		t.Errorf("env block printed %d times, want 1", n)
	}
}

// The single most useful line for a host run, and the one local never had: what actually ran.
func TestLogLocalEvalStep_EchoesArgvAndCwd(t *testing.T) {
	stubToolsOnPATH(t, "mvn")
	dir := javaRepoFixture(t)
	cmd, err := localBuildCommand(dir, "test", "mvn", "", "")
	if err != nil {
		t.Fatal(err)
	}
	out := captureRunnerStderr(t, func() { logLocalEvalStep(evaluator.StepTest, cmd) })
	for _, want := range []string{"[asqs-eval] step=test phase=main", "argv=[mvn -q -B test]", "cwd=" + dir} {
		if !strings.Contains(out, want) {
			t.Errorf("step line missing %q, got %q", want, out)
		}
	}
}
