package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
)

// nodeProbeTimeout bounds the runtime version probe. It is one `node -p`, but in Docker it is also
// a container start, so it gets its own small budget rather than the 15-minute install one.
const nodeProbeTimeout = 2 * time.Minute

// detectNodeVersion asks the environment the generated tests will actually run in — the bootstrap
// container when there is one, the host otherwise — which Node it has.
//
// The image tag cannot answer this: `node:lts` moves between majors, client images are opaque, and a
// host bootstrap has no image at all. An empty return means "not determined", which every caller
// reads as a reason to be conservative rather than a reason to fail.
func detectNodeVersion(ctx context.Context, ed *EphemeralDocker, repo string) string {
	pCtx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	defer cancel()
	out, err := RunArgv(pCtx, ed, repo, []string{"node", "-p", "process.versions.node"}, nil)
	if err != nil {
		return ""
	}
	// Last non-empty line: `node -p` prints only the version, but a container entrypoint may not.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if v := strings.TrimSpace(lines[i]); v != "" {
			return v
		}
	}
	return ""
}

// resolveJSTestProfile reads the package the bootstrap will patch and derives the required stack.
// nodeVersion is the runtime the stack has to load on; "" when it could not be determined.
func resolveJSTestProfile(repo, lang, nodeVersion string) (jsTestProfile, string, error) {
	pkgDir, err := resolveJSPackageDirForBootstrap(repo)
	if err != nil {
		return jsTestProfile{}, "", err
	}
	det, err := detectJSFramework(pkgDir, lang)
	if err != nil {
		return jsTestProfile{}, pkgDir, err
	}
	return buildJSTestProfile(det, nodeVersion), pkgDir, nil
}

func runJSBootstrap(ctx context.Context, repo string, cfg *config.TestFrameworkBootstrapConfig, lang string, audit Auditor, runnerTimeout string, ed *EphemeralDocker) error {
	nodeVersion := detectNodeVersion(ctx, ed, repo)
	prof, pkgDir, err := resolveJSTestProfile(repo, lang, nodeVersion)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	pkgPath := filepath.Join(pkgDir, "package.json")

	logAudit(audit, ctx, "test_bootstrap.js_profile", map[string]interface{}{
		"message": fmt.Sprintf("Detected %s; required stack: %s.",
			summarizeJSProfile(prof), strings.Join(describeJSDeps(prof.Deps), ", ")),
		"framework":         string(prof.Framework),
		"framework_version": prof.FrameworkVersion,
		"runner":            string(prof.Runner),
		"test_environment":  prof.TestEnvironment,
		"typescript":        prof.IsTS,
		"esm":               prof.IsESM,
		"vite_major":        prof.ViteMajor,
		"evidence":          prof.Evidence,
		"stack":             prof.Stack,
		"required_deps":     describeJSDeps(prof.Deps),
		"package_root":      relPathForBootstrap(repo, pkgDir),
		// The runtime is a stack input, not a detail: jsdom's line is chosen from it, and its
		// absence from this event is why a jsdom/Node mismatch previously needed a Docker repro to
		// diagnose from audit.log alone.
		"node_version": nodeVersion,
	})

	if prof.Declined {
		// The runtime decline is a different fault from the framework ones: nothing about the repo
		// is wrong, so the reason code has to point at the environment or the audit sends whoever
		// reads it to the wrong file.
		reason := "framework_unsupported"
		if prof.Stack == jsStackJsdomDeclined {
			reason = "runtime_unsupported"
		}
		logAudit(audit, ctx, "test_bootstrap.skip_framework_unsupported", map[string]interface{}{
			"message":      prof.DeclinedReason,
			"framework":    string(prof.Framework),
			"reason":       reason,
			"node_version": nodeVersion,
		})
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: skipped — %s\n", prof.DeclinedReason)
		return nil
	}

	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message": fmt.Sprintf("Installing the %s stack (%s, testEnvironment=%s)", prof.Stack, prof.Runner, prof.TestEnvironment),
		"stack":   prof.Stack,
		"runner":  string(prof.Runner),
	})

	pkg, err := readJSPackageJSON(pkgDir)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	missing := prof.missingDeps(pkg)

	addedScript, err := mergeJSTestDepsIntoPackageJSON(pkgPath, prof, missing, cfg.PinVersions)
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to update package.json: %v", err),
			"step":    "merge_package_json",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap package.json: %w", err)
	}
	filesChanged := []string{relPathForBootstrap(repo, pkgPath)}

	configRel, wroteConfig, err := writeJSRunnerConfig(pkgDir, prof)
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write %s: %v", configRel, err),
			"step":    "runner_config",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap runner config: %w", err)
	}
	if wroteConfig {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, filepath.Join(pkgDir, configRel)))
	}

	tsSpecRel, wroteTSSpec, err := writeAngularTsconfigSpec(pkgDir, prof)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap tsconfig.spec.json: %w", err)
	}
	if wroteTSSpec {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, filepath.Join(pkgDir, tsSpecRel)))
	}

	setupRel, wroteSetup, err := writeJSSetupFile(pkgDir, prof)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap setup file: %w", err)
	}
	if wroteSetup {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, filepath.Join(pkgDir, setupRel)))
	}

	if prof.IsTS {
		tsconfigPatches, terr := ensureTestTypeScriptTooling(repo, pkgDir, prof)
		if terr != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to set up TypeScript test globals: %v", terr),
				"step":    "jest_tsconfig",
				"error":   terr.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap typescript: %w", terr)
		}
		filesChanged = append(filesChanged, tsconfigPatches...)
	}

	unitSmoke, err := writeJSUnitSmokeTest(pkgDir, prof)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	if unitSmoke.Wrote {
		filesChanged = append(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))
	}

	logAudit(audit, ctx, "test_bootstrap.patched", map[string]interface{}{
		"message":       fmt.Sprintf("Patched: %s (added %s; script %s)", strings.Join(filesChanged, ", "), strings.Join(describeJSDeps(missing), ", "), addedScript),
		"files_changed": filesChanged,
		"added_deps":    describeJSDeps(missing),
		"test_script":   addedScript,
	})

	npmWorkdir := npmInstallWorkdir(repo, pkgDir)
	if err := installJSDependencies(ctx, repo, npmWorkdir, cfg, audit, ed, runnerTimeout); err != nil {
		return err
	}

	runner := jsGoalRunner{repo: repo, workdir: pkgDir, profile: prof, ed: ed, timeout: installTimeout(runnerTimeout)}

	// Mandatory gate: the runner must start, transform this file and execute the assertion.
	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Running the bootstrap smoke test with %s", prof.Runner),
		"command": runner.describe(runner.testArgv(unitSmoke.Rel)),
	})
	if cmdLine, out, rerr := runner.runTestFile(ctx, unitSmoke.Rel); rerr != nil {
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("The %s stack does not execute a test in this package: %v. "+
				"Generated tests cannot run here, and the fix loop may not edit package.json or runner config — stopping now.", prof.Stack, rerr),
			"step":          "smoke_test",
			"command":       cmdLine,
			"stack":         prof.Stack,
			"required_deps": describeJSDeps(prof.Deps),
			"output":        truncate(string(out), 8000),
			"error":         rerr.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap verify %s: %w\n%s", prof.Runner, rerr, truncate(string(out), 4000))
	}

	frameworkOK, note := runJSFrameworkSmoke(ctx, runner, audit, repo, pkgDir, prof, &filesChanged)

	// Second mandatory gate for TypeScript: the stack must COMPILE a generated test, not only run
	// one. See js_typecheck.go — the runner gate above cannot see a missing type declaration,
	// because the libraries it exercises work at run time regardless.
	typecheckNote, terr := runJSTypecheckGateAudited(ctx, runner, audit, pkgDir, prof)
	if terr != nil {
		removeJSSmokeFile(unitSmoke)
		return terr
	}
	if typecheckNote != "" {
		note += typecheckNote
	}

	// Instruments, not deliverables — see the Java path for the reasoning. An ESLint or Prettier
	// check in the project's own pipeline has the same claim on a foreign file that
	// spring-javaformat had on the Java one.
	removeJSSmokeFile(unitSmoke)
	filesChanged = removeRelPath(filesChanged, relPathForBootstrap(repo, unitSmoke.Abs))

	contract := jsContract(prof, lang)
	contract.Verified = true
	contract.Smoke = smokeFromRun(string(prof.FrameworkSmoke), prof.FrameworkSmoke != jsSmokeNone, frameworkOK, note)
	if note != "" && !frameworkOK {
		contract.Notes = append(contract.Notes, strings.TrimSpace(note))
	}
	// The contract goes at the workspace root, not the package root: consumers resolve it from the
	// repo path they were given, which is the workspace even in a nested-package layout.
	writeTestStackContract(ctx, audit, repo, contract)

	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":              fmt.Sprintf("%s bootstrap ok; %s passed%s", prof.Stack, unitSmoke.Rel, note),
		"files_changed":        filesChanged,
		"stack":                prof.Stack,
		"runner":               string(prof.Runner),
		"framework":            string(prof.Framework),
		"package_root":         relPathForBootstrap(repo, pkgDir),
		"unit_smoke":           unitSmoke.Rel,
		"framework_smoke_ok":   frameworkOK,
		"framework_smoke_note": strings.TrimSpace(note),
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: %s ok; %s passed%s\n", prof.Stack, unitSmoke.Rel, note)
	return nil
}

// runJSFrameworkSmoke stages and runs the framework-representative test.
//
// Always advisory. The unit smoke has already proven the runner and transform work; a framework smoke
// that fails means app-specific wiring (Angular's zone setup, a JSX pragma, Nest's decorator
// metadata) needs attention a manifest patcher cannot infer, and unit tests remain useful. A file
// this run wrote and could not get passing is removed so the evaluator never inherits it.
func runJSFrameworkSmoke(ctx context.Context, runner jsGoalRunner, audit Auditor, repo, pkgDir string, prof jsTestProfile, filesChanged *[]string) (ok bool, note string) {
	smoke, extra, staged, err := writeJSFrameworkSmokeTest(pkgDir, prof)
	if err != nil {
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_skipped", map[string]interface{}{
			"message":   fmt.Sprintf("Could not stage the %s framework smoke test: %v. Continuing with the runner stack.", prof.Framework, err),
			"framework": string(prof.Framework),
			"reason":    "stage_failed",
		})
		return false, ""
	}
	if !staged {
		return false, ""
	}

	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": fmt.Sprintf("Running the %s framework smoke test", prof.Framework),
		"command": runner.describe(runner.testArgv(smoke.Rel)),
	})
	if cmdLine, out, rerr := runner.runTestFile(ctx, smoke.Rel); rerr != nil {
		removeJSSmokeFile(smoke)
		for _, f := range extra {
			removeJSSmokeFile(f)
		}
		logAudit(audit, ctx, "test_bootstrap.framework_smoke_failed", map[string]interface{}{
			"message": fmt.Sprintf("The %s framework smoke test does not pass: %v. The runner stack is verified, so generation continues; "+
				"framework-specific tests are unlikely to pass until the app wiring is fixed.", prof.Framework, rerr),
			"framework": string(prof.Framework),
			"reason":    "framework_smoke_failed",
			"command":   cmdLine,
			"output":    truncate(string(out), 8000),
			"error":     rerr.Error(),
		})
		return false, fmt.Sprintf(" (%s framework smoke failed; runner stack only)", prof.Framework)
	}

	removeJSSmokeFile(smoke)
	for _, f := range extra {
		removeJSSmokeFile(f)
	}
	return true, fmt.Sprintf(" and %s (%s wiring works)", smoke.Rel, prof.Framework)
}

// installJSDependencies runs the package manager install for the freshly merged package.json.
func installJSDependencies(ctx context.Context, repo, npmWorkdir string, cfg *config.TestFrameworkBootstrapConfig, audit Auditor, ed *EphemeralDocker, runnerTimeout string) error {
	pm := detectPackageManager(npmWorkdir)
	hasLock := hasLockfile(npmWorkdir, pm)
	pnpmStore := ""
	if pm == PMPnpm {
		if err := EnsurePnpmBootstrapGitignore(repo); err != nil {
			return fmt.Errorf("test_framework_bootstrap .gitignore: %w", err)
		}
		var err error
		pnpmStore, err = BootstrapPnpmStorePath(ed != nil)
		if err != nil {
			return fmt.Errorf("test_framework_bootstrap pnpm store path: %w", err)
		}
	}
	cmdLine := installCmdLine(pm, cfg.AllowLockfileChange, hasLock, true, pnpmStore)

	instCtx, cancel := context.WithTimeout(ctx, installTimeout(runnerTimeout))
	defer cancel()

	installMsg := fmt.Sprintf("Running %s", cmdLine)
	if ed != nil {
		installMsg = fmt.Sprintf("Running %s (docker: corepack enable; then install)", cmdLine)
	}
	logAudit(audit, ctx, "test_bootstrap.install", map[string]interface{}{
		"message": installMsg,
		"command": cmdLine,
		"pm":      string(pm),
	})

	out, err := RunPackageManagerInstall(instCtx, ed, npmWorkdir, pm, cfg.AllowLockfileChange, hasLock, true, []string{"CI=true", "NPM_CONFIG_YES=true"})
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.install_failed", map[string]interface{}{
			"message": fmt.Sprintf("Install failed: %v", err),
			"command": cmdLine,
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap install: %w\n%s", err, truncate(string(out), 4000))
	}
	auditInstallRetried(audit, ctx, "test_bootstrap.install_retried_newer_npm", cmdLine, out)
	return nil
}

// auditInstallRetried records that the image's npm crashed and the install succeeded only on the
// npx fallback (see npmFallbackSpec). Silent when no retry happened.
func auditInstallRetried(audit Auditor, ctx context.Context, step, cmdLine string, out []byte) {
	if !installOutputRetried(out) {
		return
	}
	logAudit(audit, ctx, step, map[string]interface{}{
		"message": fmt.Sprintf("%s failed with an internal npm crash (Cannot read properties of null); the install succeeded on retry with %s via npx. The image's npm has a resolver bug for this manifest; consider a newer Node image.", cmdLine, npmFallbackSpec),
		"command": cmdLine,
		"retry":   "npx --yes " + npmFallbackSpec,
		"output":  truncate(string(out), 4000),
	})
}

func isJSLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	default:
		return false
	}
}
