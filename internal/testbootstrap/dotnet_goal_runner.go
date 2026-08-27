package testbootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/runner"
)

// dotnetGoalRunner builds and runs a single test class for one .csproj, on the host or inside the
// ephemeral bootstrap container, reporting the command line so audit payloads stay actionable.
//
// It mirrors javaGoalRunner. The reason both exist rather than one abstraction is that the argv
// shaping is genuinely different: .NET needs TargetFramework fallback props threaded through every
// invocation (see runner.AppendDotnetMultiTargetFrameworkArgv), Java does not.
type dotnetGoalRunner struct {
	repo        string
	csprojAbs   string
	ed          *EphemeralDocker
	timeout     time.Duration
	fallbackTFM string
}

func (r dotnetGoalRunner) buildArgv() ([]string, error) {
	rel, err := csprojRelForDotnet(r.repo, r.csprojAbs)
	if err != nil {
		return nil, err
	}
	argv := []string{"dotnet", "build", rel, "--verbosity", "quiet", "-nologo"}
	argv = runner.AppendDotnetMultiTargetFrameworkArgv(argv, r.csprojAbs, r.fallbackTFM)
	argv = appendDotnetCLIArgsTFMFallback(argv, r.csprojAbs, r.fallbackTFM)
	return runner.ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv), nil
}

// build restores and compiles the test project, separating "the packages do not resolve" from
// "the test fails".
func (r dotnetGoalRunner) build(ctx context.Context) (cmdLine string, out []byte, err error) {
	argv, err := r.buildArgv()
	if err != nil {
		return "", nil, err
	}
	bCtx, cancel := context.WithTimeout(ctx, r.effectiveTimeout())
	defer cancel()
	cmdLine = strings.Join(argv, " ")
	if r.ed == nil {
		c := exec.CommandContext(bCtx, argv[0], argv[1:]...)
		c.Dir = r.repo
		out, err = c.CombinedOutput()
		return cmdLine, out, err
	}
	out, err = RunArgv(bCtx, r.ed, r.repo, argv, nil)
	return cmdLine, out, err
}

// runTestClass executes exactly the named test class.
func (r dotnetGoalRunner) runTestClass(ctx context.Context, fullyQualifiedName string) (cmdLine string, out []byte, err error) {
	tCtx, cancel := context.WithTimeout(ctx, r.effectiveTimeout())
	defer cancel()
	filter := "FullyQualifiedName~" + fullyQualifiedName
	cmdLine = fmt.Sprintf("dotnet test %s --filter %s", r.csprojAbs, filter)
	out, err = runDotnetTestWithFilter(tCtx, r.ed, r.repo, r.csprojAbs, filter, r.fallbackTFM, "")
	return cmdLine, out, err
}

func (r dotnetGoalRunner) effectiveTimeout() time.Duration {
	if r.timeout <= 0 {
		return defaultInstallTimeout
	}
	return r.timeout
}
