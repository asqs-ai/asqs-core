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

// scriptArgv runs one package.json script through the package manager.
//
// `<pm> run <script>` rather than a hand-built `npx tsc` line: which tsconfig type-checks the app is
// the project's decision, recorded in its own script, and the evaluator's compile step runs that
// same script (runner/js_plan.go). Rebuilding the command here would let the gate and the gate it
// stands in for disagree.
func (r jsGoalRunner) scriptArgv(script string) []string {
	pm := detectPackageManager(r.workdir)
	return []string{string(pm), "run", script}
}

// runScript executes a package script and returns the command line, combined output and error.
func (r jsGoalRunner) runScript(ctx context.Context, script string) (cmdLine string, out []byte, err error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultInstallTimeout
	}
	rCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := r.scriptArgv(script)
	cmdLine = r.describe(argv)
	out, err = RunArgv(rCtx, r.ed, r.workdir, argv, []string{"CI=true", "NPM_CONFIG_YES=true"})
	if err != nil {
		return cmdLine, out, fmt.Errorf("%s: %w", script, err)
	}
	return cmdLine, out, nil
}

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
