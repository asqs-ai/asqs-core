package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
)

func runCSharpBootstrap(ctx context.Context, repo, gitRoot string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	_ = cfg // pin_versions / lockfile N/A for .NET bootstrap v1

	csproj, err := primaryCsprojAbs(repo)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: discover .csproj: %w", err)
	}
	if csproj == "" {
		return fmt.Errorf("test_framework_bootstrap: no SDK-style .csproj found under repo")
	}

	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message": "Ensuring xUnit + Microsoft.NET.Test.Sdk on selected .csproj (solution-first when root .sln / .slnx exists)",
		"stack":   "xunit",
		"csproj":  filepath.Base(csproj),
	})

	changedPaths, err := applyCSharpXUnit(repo, csproj, gitRoot)
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to patch .csproj: %v", err),
			"step":    "csproj",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap csproj: %w", err)
	}

	var filesChanged []string
	for _, abs := range changedPaths {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, abs))
	}
	if len(filesChanged) > 0 {
		logAudit(audit, ctx, "test_bootstrap.patched", map[string]interface{}{
			"message":       fmt.Sprintf("Patched: %s", strings.Join(filesChanged, ", ")),
			"files_changed": filesChanged,
		})
	}

	timeout := installTimeout(runnerTimeout)
	vCtx, vCancel := context.WithTimeout(ctx, timeout)
	defer vCancel()

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Verifying with dotnet test %s", filepath.Base(csproj)),
		"command": fmt.Sprintf("dotnet test %s", filepath.Base(csproj)),
	})

	out, err := runDotnetTest(vCtx, ed, repo, csproj, dotnetTFMFallbackFromRunner(runnerCfg))
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("dotnet test failed: %v", err),
			"command": "dotnet test " + filepath.Base(csproj),
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap verify csharp: %w\n%s", err, truncate(string(out), 4000))
	}

	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":       fmt.Sprintf("xUnit bootstrap ok; dotnet test passed (%s)", filepath.Base(csproj)),
		"files_changed": filesChanged,
		"stack":         "xunit",
	})
	if len(filesChanged) > 0 {
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: added xUnit; dotnet test ok (%s)\n", filepath.Base(csproj))
	} else {
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: test packages already present; dotnet test ok (%s)\n", filepath.Base(csproj))
	}
	return nil
}
