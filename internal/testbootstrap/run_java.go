package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/javaproj"
)

// resolveJavaTestProfile reads the module's primary build file and derives the required test stack.
//
// Pure file reads, so both detection and application can call it without coordinating.
func resolveJavaTestProfile(repo string) (prof javaTestProfile, jbf javaBuildPick, err error) {
	jbf, err = primaryJavaBuildFile(repo)
	if err != nil {
		return javaTestProfile{}, jbf, err
	}
	if jbf.Abs == "" {
		return javaTestProfile{}, jbf, nil
	}
	b, err := os.ReadFile(jbf.Abs)
	if err != nil {
		return javaTestProfile{}, jbf, err
	}
	src := string(b)

	var det javaFrameworkDetection
	if jbf.Kind == javaBuildMaven {
		det = detectJavaFrameworkMaven(src)
	} else {
		det = detectJavaFrameworkGradle(src)
	}

	// The Java language level decides which Mockito line can actually load.
	moduleRel, _ := filepath.Rel(repo, filepath.Dir(jbf.Abs))
	facts := javaproj.Resolve(repo, filepath.ToSlash(filepath.Join(moduleRel, "src", "main", "java")))
	return buildJavaTestProfile(det, facts.JavaVersion), jbf, nil
}

func runJavaBootstrap(ctx context.Context, repo string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, runnerTimeout string, ed *EphemeralDocker) error {
	_ = cfg // pin_versions / lockfile semantics do not apply to Maven/Gradle coordinates

	prof, jbf, err := resolveJavaTestProfile(repo)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: discover Java build: %w", err)
	}
	if jbf.Abs == "" {
		return fmt.Errorf("test_framework_bootstrap: no pom.xml or build.gradle(.kts) at repo root")
	}
	moduleRoot := filepath.Dir(jbf.Abs)

	logAudit(audit, ctx, "test_bootstrap.java_profile", map[string]interface{}{
		"message": fmt.Sprintf("Detected %s; required test stack: %s.",
			summarizeJavaProfile(prof), strings.Join(describeJavaDeps(prof.Deps), ", ")),
		"framework":         string(prof.Framework),
		"framework_version": prof.FrameworkVersion,
		"version_managed":   prof.VersionManaged,
		"evidence":          prof.Evidence,
		"stack":             prof.Stack,
		"required_deps":     describeJavaDeps(prof.Deps),
		"build_file":        relPathForBootstrap(repo, jbf.Abs),
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

	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message": fmt.Sprintf("Ensuring the %s test stack on %s", prof.Stack, relPathForBootstrap(repo, jbf.Abs)),
		"stack":   prof.Stack,
	})

	var (
		filesChanged []string
		added        []javaDep
		changed      bool
	)
	switch jbf.Kind {
	case javaBuildMaven:
		changed, added, err = applyMavenTestDeps(jbf.Abs, prof)
	case javaBuildGradleGroovy:
		changed, added, err = applyGradleTestDeps(jbf.Abs, false, prof)
	case javaBuildGradleKotlin:
		changed, added, err = applyGradleTestDeps(jbf.Abs, true, prof)
	default:
		return fmt.Errorf("test_framework_bootstrap: unknown Java build kind")
	}
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to patch %s: %v", relPathForBootstrap(repo, jbf.Abs), err),
			"step":    "java_manifest",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap java manifest: %w", err)
	}
	if changed {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, jbf.Abs))
		logAudit(audit, ctx, "test_bootstrap.patched", map[string]interface{}{
			"message":       fmt.Sprintf("Patched: %s (added %s)", strings.Join(filesChanged, ", "), strings.Join(describeJavaDeps(added), ", ")),
			"files_changed": filesChanged,
			"added_deps":    describeJavaDeps(added),
		})
	}

	unitSmoke, err := writeJavaUnitSmokeTest(moduleRoot)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	if unitSmoke.Wrote {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))
	}

	runner := javaGoalRunner{repo: repo, build: jbf, ed: ed, timeout: installTimeout(runnerTimeout)}

	// Mandatory gate. Compiling proves the coordinates resolve; running proves the runner is wired
	// (on Gradle, a missing useJUnitPlatform() compiles perfectly and executes nothing).
	verified, err := runJavaSmokeGate(ctx, runner, audit, unitSmoke, prof)
	if err != nil {
		removeJavaSmokeFile(unitSmoke)
		return err
	}

	frameworkSmokeOK, frameworkSmokeNote := false, ""
	if verified {
		frameworkSmokeOK, frameworkSmokeNote = runJavaFrameworkSmoke(ctx, runner, audit, repo, moduleRoot, prof, &filesChanged)
	}

	// The smoke tests are instruments, not deliverables. Once they have answered the question they
	// must not survive into the repository: they are foreign files in someone else's build, and a
	// project that validates formatting or style on every compile — spring-petclinic validates
	// spring-javaformat — would reject them for the rest of the run, in a file the fix loop has no
	// mandate to repair.
	removeJavaSmokeFile(unitSmoke)
	filesChanged = removeRelPath(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))

	contract := javaContract(prof)
	contract.Verified = verified
	if verified {
		// Only the sources that actually compiled and ran. The framework smoke is included only
		// when it passed: a staged-but-failed one proves nothing about its imports.
		sources := []string{javaUnitSmokeClass}
		if frameworkSmokeOK {
			sources = append(sources, javaFrameworkSmokeSource(prof.FrameworkSmoke))
		}
		contract.VerifiedImports = smokeVerifiedImports(sources...)
	}
	contract.Smoke = smokeFromRun(string(prof.FrameworkSmoke), prof.FrameworkSmoke != javaSmokeNone, frameworkSmokeOK, frameworkSmokeNote)
	if frameworkSmokeNote != "" && !frameworkSmokeOK {
		contract.Notes = append(contract.Notes, strings.TrimSpace(frameworkSmokeNote))
	}
	if !verified {
		contract.Notes = append(contract.Notes,
			"The stack was installed but not proven: the build failed a formatter/linter check before the smoke test could run.")
	}
	writeTestStackContract(ctx, audit, repo, contract)

	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message": fmt.Sprintf("%s bootstrap ok; %s passed%s",
			prof.Stack, unitSmoke.FQCN, frameworkSmokeNote),
		"files_changed":        filesChanged,
		"stack":                prof.Stack,
		"framework":            string(prof.Framework),
		"unit_smoke":           unitSmoke.FQCN,
		"framework_smoke_ok":   frameworkSmokeOK,
		"framework_smoke_note": strings.TrimSpace(frameworkSmokeNote),
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: %s ok; %s passed%s\n", prof.Stack, unitSmoke.FQCN, frameworkSmokeNote)
	return nil
}

// runJavaSmokeGate compiles and runs the unit smoke test. Any failure ends the run.
//
// Failing here at minute one, naming the dependency, is the whole point: the alternative — observed
// in a real run — is twelve generated artifacts, 102 compile errors, seven fix attempts and 29
// minutes, all tracing back to one absent test-scoped dependency.
func runJavaSmokeGate(ctx context.Context, runner javaGoalRunner, audit Auditor, smoke javaSmokeFile, prof javaTestProfile) (verified bool, err error) {
	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": "Resolving the test classpath and compiling the bootstrap smoke test",
		"command": runner.describe(runner.compileGoals()),
	})
	if cmdLine, out, cerr := runner.run(ctx, runner.compileGoals()...); cerr != nil {
		// A formatter or linter rejecting the smoke test is not evidence about the test classpath.
		// Most of them are bound to `validate`, which runs BEFORE compile, so the question bootstrap
		// asks was never reached — and aborting the run over a javadoc wrapping rule is far worse
		// than the missing-dependency failure this gate exists to catch.
		if isStyleViolationFailure(string(out)) {
			logAudit(audit, ctx, "test_bootstrap.verify_style_violation", map[string]interface{}{
				"message":     styleViolationRemediation(string(out)),
				"step":        "smoke_compile",
				"command":     cmdLine,
				"stack":       prof.Stack,
				"remediation": styleViolationRemediation(string(out)),
				"output":      truncate(string(out), 8000),
			})
			return false, nil
		}
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("Test classpath does not resolve for the %s stack: %v. "+
				"Generated tests cannot compile here, and the fix loop may not edit build manifests — stopping now.", prof.Stack, cerr),
			"step":          "smoke_compile",
			"command":       cmdLine,
			"stack":         prof.Stack,
			"required_deps": describeJavaDeps(prof.Deps),
			"output":        truncate(string(out), 8000),
			"error":         cerr.Error(),
		})
		return false, fmt.Errorf("test_framework_bootstrap verify java compile: %w\n%s", cerr, truncate(string(out), 4000))
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": "Running the bootstrap smoke test (JUnit 5 + Mockito + AssertJ)",
		"command": runner.describe(runner.singleTestGoals(smoke.FQCN)),
	})
	if cmdLine, out, terr := runner.run(ctx, runner.singleTestGoals(smoke.FQCN)...); terr != nil {
		if isStyleViolationFailure(string(out)) {
			logAudit(audit, ctx, "test_bootstrap.verify_style_violation", map[string]interface{}{
				"message":     styleViolationRemediation(string(out)),
				"step":        "smoke_test",
				"command":     cmdLine,
				"stack":       prof.Stack,
				"remediation": styleViolationRemediation(string(out)),
				"output":      truncate(string(out), 8000),
			})
			return false, nil
		}
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("Bootstrap smoke test %s failed: %v. The test stack compiles but does not execute.", smoke.FQCN, terr),
			"step":    "smoke_test",
			"command": cmdLine,
			"stack":   prof.Stack,
			"output":  truncate(string(out), 8000),
			"error":   terr.Error(),
		})
		return false, fmt.Errorf("test_framework_bootstrap verify java smoke: %w\n%s", terr, truncate(string(out), 4000))
	}
	return true, nil
}

// runJavaFrameworkSmoke stages and runs the framework-representative test, returning whether it
// passed and a short note for the completion message.
//
// Confidence differs by framework, so the failure policy does too:
//   - Spring Boot: the dependency set is exact, so a smoke test that will not COMPILE means generated
//     @SpringBootTest / @WebMvcTest classes cannot compile either. That is a hard failure.
//   - Quarkus / Micronaut: both need annotation-processor wiring that a manifest patcher cannot infer
//     safely, so a compile failure downgrades to unit-only rather than aborting a run that can still
//     produce useful unit tests.
//   - Any framework: a test that compiles but does not RUN is an environment fact (no database, no
//     port), never a stack fact. Always advisory.
//
// A framework smoke this run created and could not get passing is removed, so the evaluator never
// inherits a permanently broken file.
func runJavaFrameworkSmoke(ctx context.Context, runner javaGoalRunner, audit Auditor, repo, moduleRoot string, prof javaTestProfile, filesChanged *[]string) (ok bool, note string) {
	if prof.FrameworkSmoke == javaSmokeNone {
		return false, ""
	}
	smoke, staged, err := writeJavaFrameworkSmokeTest(moduleRoot, prof.FrameworkSmoke)
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
			"message":   fmt.Sprintf("No %s application class found under src/main/java; this module is a library, so there is no application context to boot.", prof.Framework),
			"framework": string(prof.Framework),
			"reason":    "no_application_class",
		})
		return false, ""
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Compiling the %s framework smoke test (%s)", prof.Framework, smoke.FQCN),
		"command": runner.describe(runner.compileGoals()),
	})
	if cmdLine, out, err := runner.run(ctx, runner.compileGoals()...); err != nil {
		removeJavaSmokeFile(smoke)
		payload := map[string]interface{}{
			"message":   fmt.Sprintf("The %s framework smoke test does not compile: %v. Generated integration-style tests would fail the same way.", prof.Framework, err),
			"step":      "framework_smoke_compile",
			"command":   cmdLine,
			"framework": string(prof.Framework),
			"output":    truncate(string(out), 8000),
			"error":     err.Error(),
		}
		if prof.FrameworkSmokeRequired {
			logAuditError(audit, ctx, "test_bootstrap.verify_failed", payload)
			return false, ""
		}
		payload["message"] = fmt.Sprintf("The %s framework smoke test does not compile; continuing with the unit stack only. "+
			"%s needs annotation-processor wiring that bootstrap does not attempt — configure it manually for integration-style tests.", prof.Framework, prof.Framework)
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_unsupported", payload)
		return false, " (framework smoke unsupported; unit stack only)"
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Running the %s framework smoke test (%s)", prof.Framework, smoke.FQCN),
		"command": runner.describe(runner.singleTestGoals(smoke.FQCN)),
	})
	if cmdLine, out, err := runner.run(ctx, runner.singleTestGoals(smoke.FQCN)...); err != nil {
		removeJavaSmokeFile(smoke)
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_failed", map[string]interface{}{
			"message": fmt.Sprintf("The %s application context does not start in this environment: %v. "+
				"The unit stack is verified, so generation continues; integration-style tests are unlikely to pass here.", prof.Framework, err),
			"framework": string(prof.Framework),
			"reason":    "context_start_failed",
			"command":   cmdLine,
			"output":    truncate(string(out), 8000),
			"error":     err.Error(),
		})
		return false, " (application context does not start here; unit stack only)"
	}

	// Removed on success as well as on failure: it has proven the context loads, and leaving a
	// foreign file in the repository risks the project's own build rejecting it later.
	removeJavaSmokeFile(smoke)
	return true, fmt.Sprintf(" and %s (%s context loads)", smoke.FQCN, prof.Framework)
}
