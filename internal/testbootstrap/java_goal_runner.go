package testbootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// javaGoalRunner executes Maven or Gradle goals for one module, on the host or inside the ephemeral
// bootstrap container, and reports the command line it used so audit payloads stay actionable.
type javaGoalRunner struct {
	repo    string
	build   javaBuildPick
	ed      *EphemeralDocker
	timeout time.Duration
}

// compileGoals compiles test sources without running them, separating "the classpath does not
// resolve" from "the test fails".
func (r javaGoalRunner) compileGoals() []string {
	if r.build.Kind == javaBuildMaven {
		return append([]string{"test-compile"}, mavenLintSkipProps...)
	}
	return []string{"testClasses"}
}

// singleTestGoals runs exactly one test class.
//
// -DfailIfNoSpecifiedTests=false keeps Surefire from failing the build in multi-module layouts where
// the selector matches nothing in a sibling module.
func (r javaGoalRunner) singleTestGoals(fqcn string) []string {
	if r.build.Kind == javaBuildMaven {
		return append([]string{"test", "-Dtest=" + fqcn, "-DfailIfNoSpecifiedTests=false"}, mavenLintSkipProps...)
	}
	return []string{"test", "--tests", fqcn}
}

// describe renders the command line without executing it, for pre-flight audit entries.
func (r javaGoalRunner) describe(goals []string) string {
	if r.ed != nil {
		if argv, ok := r.dockerArgv(goals); ok {
			return strings.Join(argv, " ")
		}
		return strings.Join(goals, " ")
	}
	name, prefix, ok := r.localCmd()
	if !ok {
		return strings.Join(goals, " ")
	}
	return name + " " + strings.Join(append(append([]string(nil), prefix...), goals...), " ")
}

func (r javaGoalRunner) localCmd() (name string, prefix []string, ok bool) {
	if r.build.Kind == javaBuildMaven {
		return mvnCmd(r.repo, r.build.Abs)
	}
	return gradleCmd(r.repo, r.build.Abs)
}

func (r javaGoalRunner) dockerArgv(goals []string) ([]string, bool) {
	if r.build.Kind == javaBuildMaven {
		return mavenDockerArgv(r.repo, r.build.Abs, goals...)
	}
	return gradleDockerArgv(r.repo, r.build.Abs, goals...)
}

// run executes the goals and returns the command line, combined output, and the process error.
func (r javaGoalRunner) run(ctx context.Context, goals ...string) (cmdLine string, out []byte, err error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultInstallTimeout
	}
	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if r.ed != nil {
		argv, ok := r.dockerArgv(goals)
		if !ok {
			return "", nil, fmt.Errorf("could not build a Java command for %s", r.build.Abs)
		}
		cmdLine = strings.Join(argv, " ")
		out, err = RunArgv(rCtx, r.ed, r.repo, argv, nil)
		return cmdLine, out, err
	}

	name, prefix, ok := r.localCmd()
	if !ok {
		return "", nil, fmt.Errorf("could not build a Java command for %s", r.build.Abs)
	}
	full := append(append([]string(nil), prefix...), goals...)
	cmdLine = name + " " + strings.Join(full, " ")
	cmd := exec.CommandContext(rCtx, name, full...)
	cmd.Dir = r.repo
	out, err = cmd.CombinedOutput()
	return cmdLine, out, err
}
