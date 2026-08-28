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

// resolveCSharpTestProfile reads the solution's projects and derives the required test stack.
func resolveCSharpTestProfile(repo, fallbackTFM string) (csharpTestProfile, error) {
	det, err := detectCSharpFramework(repo, fallbackTFM)
	if err != nil {
		return csharpTestProfile{}, err
	}
	return buildCSharpTestProfile(det), nil
}

func runCSharpBootstrap(ctx context.Context, repo, gitRoot string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	err := setupCSharpTestProject(ctx, repo, gitRoot, cfg, audit, runnerTimeout, ed, runnerCfg)
	// Always attempt to relocate stray tests that landed inside production projects (e.g. generated
	// before dedicated placement existed) — even when setup returned an error (the project usually
	// still exists), so production projects compile. Best-effort.
	relocateStrayCSharpTests(ctx, repo, audit)
	return err
}

func setupCSharpTestProject(ctx context.Context, repo, gitRoot string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker, runnerCfg *config.RunnerConfig) error {
	_ = cfg // pin_versions / lockfile N/A for .NET bootstrap

	fallbackTFM := dotnetTFMFallbackFromRunner(runnerCfg)
	prof, err := resolveCSharpTestProfile(repo, fallbackTFM)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: resolve C# profile: %w", err)
	}

	logAudit(audit, ctx, "test_bootstrap.csharp_profile", map[string]interface{}{
		"message": fmt.Sprintf("Detected %s; required test stack: %s.",
			summarizeCSharpProfile(prof), strings.Join(describeCSharpPackages(prof.Packages), ", ")),
		"framework":         string(prof.Framework),
		"test_framework":    string(prof.TestFramework),
		"target_framework":  prof.TargetFramework,
		"uses_ef_core":      prof.UsesEFCore,
		"evidence":          prof.Evidence,
		"stack":             prof.Stack,
		"required_packages": describeCSharpPackages(prof.Packages),
	})

	if prof.Declined {
		logAudit(audit, ctx, "test_bootstrap.skip_framework_unsupported", map[string]interface{}{
			"message":   prof.DeclinedReason,
			"framework": string(prof.Framework),
			"reason":    "framework_unsupported",
		})
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: skipped — %s\n", prof.DeclinedReason)
		return nil
	}

	var (
		testProj     string
		filesChanged []string
		added        []csharpPkg
	)

	// When the repo has production projects but no UNIT test project yet, create a dedicated test
	// project under a tests/ root (referencing the production projects) instead of patching a
	// production .csproj. DetectCSharpUnitTestProjectDir excludes E2E/Playwright projects so a
	// dedicated e2e/ project never suppresses unit creation.
	prod, _, derr := splitCSharpProdAndTestCsprojs(repo)
	if derr == nil && len(prod) > 0 && layout.DetectCSharpUnitTestProjectDir(repo) == "" {
		logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
			"message":  fmt.Sprintf("No test project found — creating a dedicated %s test project under tests/ referencing the production projects", prof.Stack),
			"stack":    prof.Stack,
			"projects": len(prod),
		})
		var changed []string
		testProj, changed, err = createDedicatedCSharpTestProject(repo, gitRoot, prod, fallbackTFM, prof)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to create dedicated test project: %v", err),
				"step":    "create_test_project",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap create csharp test project: %w", err)
		}
		for _, abs := range changed {
			filesChanged = append(filesChanged, relPathForBootstrap(repo, abs))
		}
		added = prof.Packages
		// Add it to any root solution so the evaluator's `dotnet test <solution>` builds + runs it.
		addCSharpTestProjectToSolutions(ctx, ed, repo, testProj, audit)
	} else {
		testProj, err = primaryCsprojAbs(repo)
		if err != nil {
			return fmt.Errorf("test_framework_bootstrap: discover .csproj: %w", err)
		}
		if testProj == "" {
			return fmt.Errorf("test_framework_bootstrap: no SDK-style .csproj found under repo")
		}
		logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
			"message": fmt.Sprintf("Ensuring the %s test stack on %s", prof.Stack, filepath.Base(testProj)),
			"stack":   prof.Stack,
			"csproj":  filepath.Base(testProj),
		})
		var changed []string
		changed, added, err = applyCSharpTestPackages(repo, testProj, gitRoot, prof)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to patch .csproj: %v", err),
				"step":    "csproj",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap csproj: %w", err)
		}
		for _, abs := range changed {
			filesChanged = append(filesChanged, relPathForBootstrap(repo, abs))
		}
	}

	if len(filesChanged) > 0 {
		logAudit(audit, ctx, "test_bootstrap.patched", map[string]interface{}{
			"message":        fmt.Sprintf("Patched: %s (added %s)", strings.Join(filesChanged, ", "), strings.Join(describeCSharpPackages(added), ", ")),
			"files_changed":  filesChanged,
			"added_packages": describeCSharpPackages(added),
		})
	}

	testProjDir := filepath.Dir(testProj)
	unitSmoke, err := writeCSharpUnitSmokeTest(testProjDir, prof.TestFramework)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	if unitSmoke.Wrote {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))
	}

	runner := dotnetGoalRunner{repo: repo, csprojAbs: testProj, ed: ed, timeout: installTimeout(runnerTimeout), fallbackTFM: fallbackTFM}

	if err := runCSharpSmokeGate(ctx, runner, audit, unitSmoke, prof); err != nil {
		return err
	}

	frameworkSmokeOK, frameworkSmokeNote := runCSharpFrameworkSmoke(ctx, runner, audit, repo, testProjDir, prof, &filesChanged)

	// Instruments, not deliverables — see the Java path for the reasoning.
	removeCSharpSmokeFile(unitSmoke)
	filesChanged = removeRelPath(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))

	contract := csharpContract(prof)
	contract.Verified = true
	contract.Smoke = smokeFromRun(string(prof.FrameworkSmoke), prof.FrameworkSmoke != csharpSmokeNone, frameworkSmokeOK, frameworkSmokeNote)
	if frameworkSmokeNote != "" && !frameworkSmokeOK {
		contract.Notes = append(contract.Notes, strings.TrimSpace(frameworkSmokeNote))
	}
	writeTestStackContract(ctx, audit, repo, contract)

	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":              fmt.Sprintf("%s bootstrap ok; %s passed%s", prof.Stack, unitSmoke.FullyQualifiedName, frameworkSmokeNote),
		"files_changed":        filesChanged,
		"stack":                prof.Stack,
		"framework":            string(prof.Framework),
		"test_project":         relPathForBootstrap(repo, testProj),
		"unit_smoke":           unitSmoke.FullyQualifiedName,
		"framework_smoke_ok":   frameworkSmokeOK,
		"framework_smoke_note": strings.TrimSpace(frameworkSmokeNote),
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: %s ok; %s passed%s\n", prof.Stack, unitSmoke.FullyQualifiedName, frameworkSmokeNote)
	return nil
}

// runCSharpSmokeGate builds and runs the unit smoke test. Any failure ends the run.
//
// `dotnet build` on an empty test project proves only that MSBuild ran; it is the same vacuous
// verification the Java path used to do with `mvn test-compile`. Restoring the packages AND executing
// a test that uses all of them is what actually answers "can a generated test compile and run here".
func runCSharpSmokeGate(ctx context.Context, runner dotnetGoalRunner, audit Auditor, smoke csharpSmokeFile, prof csharpTestProfile) error {
	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": "Restoring the test packages and building the bootstrap smoke test",
		"command": "dotnet build " + filepath.Base(runner.csprojAbs),
	})
	if cmdLine, out, err := runner.build(ctx); err != nil {
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("Test packages do not restore or compile for the %s stack: %v. "+
				"Generated tests cannot compile here, and the fix loop may not edit project files — stopping now.", prof.Stack, err),
			"step":              "smoke_build",
			"command":           cmdLine,
			"stack":             prof.Stack,
			"required_packages": describeCSharpPackages(prof.Packages),
			"output":            truncate(string(out), 8000),
			"error":             err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap verify csharp build: %w\n%s", err, truncate(string(out), 4000))
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Running the bootstrap smoke test (%s + Moq + FluentAssertions)", prof.TestFramework),
		"command": "dotnet test --filter FullyQualifiedName~" + smoke.FullyQualifiedName,
	})
	if cmdLine, out, err := runner.runTestClass(ctx, smoke.FullyQualifiedName); err != nil {
		payload := map[string]interface{}{
			"message": fmt.Sprintf("Bootstrap smoke test %s failed: %v. The test stack compiles but does not execute.", smoke.FullyQualifiedName, err),
			"step":    "smoke_test",
			"command": cmdLine,
			"stack":   prof.Stack,
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
		}
		// A missing shared runtime is an environment fact the raw host output buries under URLs.
		// Name it, because "the packages are wrong" and "this machine has no .NET 8" need different
		// fixes from the operator.
		if rem := dotnetRuntimeRemediation(string(out), prof.TargetFramework); rem != "" {
			payload["message"] = fmt.Sprintf("Bootstrap smoke test %s could not run. %s", smoke.FullyQualifiedName, rem)
			payload["reason"] = "dotnet_runtime_missing"
			payload["remediation"] = rem
			logAuditError(audit, ctx, "test_bootstrap.verify_failed", payload)
			return fmt.Errorf("test_framework_bootstrap verify csharp smoke: %s: %w", rem, err)
		}
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", payload)
		return fmt.Errorf("test_framework_bootstrap verify csharp smoke: %w\n%s", err, truncate(string(out), 4000))
	}
	return nil
}

// runCSharpFrameworkSmoke stages and runs the ASP.NET Core host smoke test.
//
// Always advisory, unlike the Spring Boot equivalent. @SpringBootTest resolves its configuration
// deterministically from the package tree; WebApplicationFactory<T> needs a public type from the web
// assembly, and top-level statements make the generated Program internal — so bootstrap infers an
// entry-point type instead of knowing one. A wrong inference must not cost the whole run when the
// unit stack is already verified. Anything this run wrote and could not get passing is removed.
func runCSharpFrameworkSmoke(ctx context.Context, runner dotnetGoalRunner, audit Auditor, repo, testProjDir string, prof csharpTestProfile, filesChanged *[]string) (ok bool, note string) {
	if prof.FrameworkSmoke == csharpSmokeNone {
		return false, ""
	}
	smoke, staged, err := writeCSharpFrameworkSmokeTest(testProjDir, prof)
	if err != nil {
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_skipped", map[string]interface{}{
			"message":   fmt.Sprintf("Could not stage the %s framework smoke test: %v. Continuing with the unit stack.", prof.Framework, err),
			"framework": string(prof.Framework),
			"reason":    "stage_failed",
		})
		return false, ""
	}
	if !staged {
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_skipped", map[string]interface{}{
			"message": "No public type found in the web project, so WebApplicationFactory<T> has nothing to bind to. " +
				"Top-level statements make the generated Program class internal; expose a type (or add `public partial class Program { }`) to enable integration-style tests.",
			"framework": string(prof.Framework),
			"reason":    "no_public_entry_point_type",
		})
		return false, ""
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Building the %s framework smoke test (%s)", prof.Framework, smoke.FullyQualifiedName),
		"command": "dotnet build " + filepath.Base(runner.csprojAbs),
	})
	if cmdLine, out, err := runner.build(ctx); err != nil {
		removeCSharpSmokeFile(smoke)
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_unsupported", map[string]interface{}{
			"message": fmt.Sprintf("The %s framework smoke test does not compile; continuing with the unit stack only. "+
				"WebApplicationFactory needs a public type from the web assembly and the inferred one did not work here.", prof.Framework),
			"step":      "framework_smoke_build",
			"command":   cmdLine,
			"framework": string(prof.Framework),
			"output":    truncate(string(out), 8000),
			"error":     err.Error(),
		})
		return false, " (framework smoke unsupported; unit stack only)"
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Running the %s framework smoke test (%s)", prof.Framework, smoke.FullyQualifiedName),
		"command": "dotnet test --filter FullyQualifiedName~" + smoke.FullyQualifiedName,
	})
	if cmdLine, out, err := runner.runTestClass(ctx, smoke.FullyQualifiedName); err != nil {
		removeCSharpSmokeFile(smoke)
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_failed", map[string]interface{}{
			"message": fmt.Sprintf("The %s application host does not start in this environment: %v. "+
				"The unit stack is verified, so generation continues; integration-style tests are unlikely to pass here.", prof.Framework, err),
			"framework": string(prof.Framework),
			"reason":    "host_start_failed",
			"command":   cmdLine,
			"output":    truncate(string(out), 8000),
			"error":     err.Error(),
		})
		return false, " (application host does not start here; unit stack only)"
	}

	removeCSharpSmokeFile(smoke)
	return true, fmt.Sprintf(" and %s (%s host starts)", smoke.FullyQualifiedName, prof.Framework)
}
