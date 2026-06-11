package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/layout"
)

func runCSharpBootstrap(ctx context.Context, repo, gitRoot string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	err := setupCSharpTestProject(ctx, repo, gitRoot, cfg, audit, runnerTimeout, ed, runnerCfg)
	// Always attempt to relocate stray tests that landed inside production projects (e.g. generated
	// before dedicated placement existed) — even when setup returned an error (the project usually
	// still exists), so production projects compile. Best-effort.
	relocateStrayCSharpTests(ctx, repo, audit)
	return err
}

func setupCSharpTestProject(ctx context.Context, repo, gitRoot string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	_ = cfg // pin_versions / lockfile N/A for .NET bootstrap v1

	// When the repo has production projects but no UNIT test project yet, create a dedicated xUnit test
	// project under a tests/ root (referencing the production projects) instead of patching a
	// production .csproj. We check for a UNIT test project specifically (DetectCSharpUnitTestProjectDir
	// excludes E2E/Playwright projects) so a dedicated e2e/ project never suppresses unit creation.
	if prod, _, derr := splitCSharpProdAndTestCsprojs(repo); derr == nil && len(prod) > 0 && layout.DetectCSharpUnitTestProjectDir(repo) == "" {
		return bootstrapDedicatedCSharpTestProject(ctx, repo, gitRoot, prod, audit, ed, runnerCfg)
	}

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

// bootstrapDedicatedCSharpTestProject creates a dedicated xUnit test project (and adds it to any root
// solution) so generated C# tests compile in their own project rather than inside production projects.
// The empty project is not verified with `dotnet test` here: it has no test files yet (generation runs
// after bootstrap) and a verify would force an offline restore; the real build/test happens in eval.
func bootstrapDedicatedCSharpTestProject(ctx context.Context, repo, gitRoot string, prodCsprojs []string, audit Auditor, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message":  "No test project found — creating a dedicated xUnit test project under tests/ referencing the production projects",
		"stack":    "xunit",
		"projects": len(prodCsprojs),
	})

	testProj, changed, err := createDedicatedCSharpTestProject(repo, gitRoot, prodCsprojs, dotnetTFMFallbackFromRunner(runnerCfg))
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to create dedicated test project: %v", err),
			"step":    "create_test_project",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap create csharp test project: %w", err)
	}

	var filesChanged []string
	for _, abs := range changed {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, abs))
	}

	// Add it to any root solution so the evaluator's `dotnet test <solution>` builds + runs it.
	addCSharpTestProjectToSolutions(ctx, ed, repo, testProj, audit)

	testProjRel := relPathForBootstrap(repo, testProj)
	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":       fmt.Sprintf("Created dedicated xUnit test project %s", testProjRel),
		"files_changed": filesChanged,
		"test_project":  testProjRel,
		"stack":         "xunit",
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: created dedicated xUnit test project %s (generated C# tests will be routed there)\n", testProjRel)
	return nil
}
