// Package runner: command construction for LOCAL Java build steps.
//
// Nothing here is on the Docker path. Docker eval builds argv from the toolchain profile
// (internal/runner/profile/toolchain.go) and runs it through jobrunner, where the container image
// supplies the toolchain and CI=true comes from JobSpec.Env. These helpers exist because none of
// that is true on a host.
package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// errLocalCoverageUnavailable reports that the repository declares no coverage plugin, so a
// coverage step could only re-run the test suite and produce nothing. The planner turns it into a
// skip rather than paying for a second full test run every fix-loop iteration.
var errLocalCoverageUnavailable = errors.New("no JaCoCo plugin declared in the build file")

// newLocalBuildCmd builds the exec.Cmd for a local build step.
//
// The explicit environment comes from baseStepEnv, the same source StepPlan.Env is built from, so
// the plan cannot claim an environment the process does not get. Java adds nothing beyond the
// base; .NET does, and its steps run through the plan instead (runLocalPlannedStep).
func newLocalBuildCmd(dir string, argv []string) *exec.Cmd {
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), baseStepEnv()...)
	return c
}

// localBuildCmd is newLocalBuildCmd behind the host-toolchain preflight.
func localBuildCmd(dir string, argv []string) (*exec.Cmd, error) {
	if err := requireLocalToolchain(argv[0]); err != nil {
		return nil, err
	}
	return newLocalBuildCmd(dir, argv), nil
}

// requireLocalToolchain fails before spawning when the binary a local step needs is not on PATH.
//
// runner.type: local has no image to supply a toolchain, so this is a deployment mistake rather
// than anything the run can repair — the message therefore names the config keys an operator can
// actually change. Checking here rather than at config load is deliberate: build_tool: auto only
// resolves to mvn or gradle once the repository is on disk.
//
// Since CP32 there is no wrapper case to exempt: internal/buildtool never returns "./mvnw" or a
// "mvnw.cmd", so argv[0] is always a PATH name. The remediation no longer offers a repo wrapper
// either, because configuring one would not change what runs.
func requireLocalToolchain(bin string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("general.sandbox.type is \"local\" but %q is not on PATH (%v). Install it on this host "+
			"or set general.sandbox.type to docker", bin, err)
	}
	return nil
}

// javaBuildFileDeclaresJaCoCo reports whether the project's own build file wires JaCoCo. Same
// build-file string scan the format detection uses.
func javaBuildFileDeclaresJaCoCo(dir string) bool {
	for _, rel := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if content, ok := readFileLower(filepath.Join(dir, rel)); ok && strings.Contains(content, "jacoco") {
			return true
		}
	}
	return false
}
