package testbootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// jsGoalRunner executes the profile's runner against one test file, on the host or inside the
// ephemeral bootstrap container.
//
// It replaced `npx jest --showConfig`, which proved only that a config file parsed. A config can
// parse perfectly while the transform cannot handle TSX, the environment has no document, or the
// framework preset is absent — all of which show up only when a test actually runs.
type jsGoalRunner struct {
	repo    string
	workdir string
	profile jsTestProfile
	ed      *EphemeralDocker
	timeout time.Duration
}

// testArgv runs exactly one test file, once, without watch mode.
func (r jsGoalRunner) testArgv(rel string) []string {
	if r.profile.Runner == JSRunnerVitest {
		// `vitest run` is the non-watch form; bare `vitest` watches and never exits in CI.
		return []string{"npx", "--yes", "vitest", "run", rel}
	}
	return []string{"npx", "--yes", "jest", "--runTestsByPath", rel, "--ci"}
}

func (r jsGoalRunner) describe(argv []string) string { return strings.Join(argv, " ") }

// runTestFile executes one smoke test and returns the command line, combined output and error.
func (r jsGoalRunner) runTestFile(ctx context.Context, rel string) (cmdLine string, out []byte, err error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultInstallTimeout
	}
	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := r.testArgv(rel)
	cmdLine = r.describe(argv)
	out, err = RunArgv(rCtx, r.ed, r.workdir, argv, []string{"CI=true", "NPM_CONFIG_YES=true"})
	if err != nil {
		return cmdLine, out, fmt.Errorf("%s: %w", r.profile.Runner, err)
	}
	return cmdLine, out, nil
}
