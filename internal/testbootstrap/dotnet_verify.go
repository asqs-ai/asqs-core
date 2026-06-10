package testbootstrap

import (
	"context"
	"os/exec"
	"strings"

	"github.com/asqs/asqs-core/internal/runner"
)

// runDotnetTest runs `dotnet test` on the given .csproj from repoDir (working directory).
// csprojAbs must live under repoDir; a repo-relative path is passed to dotnet (supports nested .csproj).
// When ed is non-nil, runs inside ephemeral Docker with repo mounted at /workspace.
// dotnetFallbackTFM when non-empty may append /p:TargetFramework (see runner.dotnet_fallback_target_framework).
func runDotnetTest(ctx context.Context, ed *EphemeralDocker, repoDir, csprojAbs, dotnetFallbackTFM string) ([]byte, error) {
	return runDotnetTestWithFilter(ctx, ed, repoDir, csprojAbs, "", dotnetFallbackTFM, "")
}

// runDotnetTestWithFilter runs dotnet test with an optional --filter (e.g. FullyQualifiedName~MyTest).
// dockerShellPrefix when non-empty is prepended in the same Docker container (Playwright .NET bootstrap net6+8 images).
func runDotnetTestWithFilter(ctx context.Context, ed *EphemeralDocker, repoDir, csprojAbs, testFilter, dotnetFallbackTFM, dockerShellPrefix string) ([]byte, error) {
	rel, err := csprojRelForDotnet(repoDir, csprojAbs)
	if err != nil {
		return nil, err
	}
	// VSTest-based `dotnet test` does not support --project; the switch is forwarded to MSBuild → MSB1001.
	// Use a positional project/solution path (MTP-only `dotnet test` is opt-in via global.json).
	argv := []string{"dotnet", "test", rel, "--verbosity", "quiet", "-nologo"}
	if f := strings.TrimSpace(testFilter); f != "" {
		argv = append(argv, "--filter", f)
	}
	argv = runner.AppendDotnetMultiTargetFrameworkArgv(argv, csprojAbs, dotnetFallbackTFM)
	argv = appendDotnetCLIArgsTFMFallback(argv, csprojAbs, dotnetFallbackTFM)
	argv = runner.ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv)
	if ed == nil {
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = repoDir
		return c.CombinedOutput()
	}
	return RunArgvWithShellPrefix(ctx, ed, repoDir, argv, nil, dockerShellPrefix)
}
