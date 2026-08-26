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
	"os/exec"
	"path/filepath"
	"strings"
)

// errLocalCoverageUnavailable reports that the repository declares no coverage plugin, so a
// coverage step could only re-run the test suite and produce nothing. The planner turns it into a
// skip rather than paying for a second full test run every fix-loop iteration.
var errLocalCoverageUnavailable = errors.New("no JaCoCo plugin declared in the build file")

// newLocalBuildCmd builds the exec.Cmd for a local build step. The environment is inherited from
// the process; CP33 gives local steps the shared explicit step env the plan records.
func newLocalBuildCmd(dir string, argv []string) *exec.Cmd {
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
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
// Repo wrappers (./mvnw, ./gradlew) and shell invocations are exempt: they are paths, not PATH
// names. CP32 removes the wrapper case entirely.
func requireLocalToolchain(bin string) error {
	if bin == "sh" || strings.ContainsAny(bin, "/\\") || strings.HasSuffix(bin, ".cmd") || strings.HasSuffix(bin, ".bat") {
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("runner.type is \"local\" but %q is not on PATH (%v). Install it on this host "+
			"or set runner.type to docker", bin, err)
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
