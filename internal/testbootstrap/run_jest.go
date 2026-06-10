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

func runJestBootstrap(ctx context.Context, repo string, cfg *config.TestFrameworkBootstrapConfig, lang string, audit Auditor, runnerTimeout string, ed *EphemeralDocker) error {
	isTS := detectJestBootstrapIsTS(repo, lang)

	logAudit(audit, ctx, "test_bootstrap.apply_start", map[string]interface{}{
		"message": fmt.Sprintf("Installing default Jest stack (%s)", map[bool]string{true: "TypeScript", false: "JavaScript"}[isTS]),
		"stack":   "jest",
		"is_ts":   isTS,
	})

	pkgDir, err := resolveJSPackageDirForBootstrap(repo)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	pkgPath := filepath.Join(pkgDir, "package.json")
	if err := mergeJestIntoPackageJSON(pkgPath, isTS, cfg.PinVersions); err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to update package.json: %v", err),
			"step":    "merge_package_json",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap package.json: %w", err)
	}
	if err := writeJestConfig(pkgDir, isTS); err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write jest.config.cjs: %v", err),
			"step":    "jest_config",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap jest config: %w", err)
	}
	if err := writeJestSmokeSpec(pkgDir); err != nil {
		logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write Jest smoke spec: %v", err),
			"step":    "jest_smoke_spec",
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap jest smoke spec: %w", err)
	}
	var tsconfigPatches []string
	if isTS {
		var err error
		tsconfigPatches, err = ensureJestTypeScriptTooling(repo, pkgDir)
		if err != nil {
			logAuditError(audit, ctx, "test_bootstrap.apply_failed", map[string]interface{}{
				"message": fmt.Sprintf("Failed to set up Jest TypeScript globals: %v", err),
				"step":    "jest_tsconfig",
				"error":   err.Error(),
			})
			return fmt.Errorf("test_framework_bootstrap jest typescript: %w", err)
		}
	}

	npmWorkdir := npmInstallWorkdir(repo, pkgDir)
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
	// package.json was just merged with new deps; lockfile must refresh (pnpm: CI=true implies frozen).
	cmdLine := installCmdLine(pm, cfg.AllowLockfileChange, hasLock, true, pnpmStore)

	timeout := installTimeout(runnerTimeout)
	instCtx, cancel := context.WithTimeout(ctx, timeout)
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
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap install: %w\n%s", err, truncate(string(out), 4000))
	}

	vCtx, vCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer vCancel()
	vName, vArgs := jestVerifyArgs()
	vArgv := append([]string{vName}, vArgs...)
	vOut, vErr := RunArgv(vCtx, ed, npmWorkdir, vArgv, []string{"CI=true", "NPM_CONFIG_YES=true"})
	if vErr != nil {
		logAuditError(audit, ctx, "test_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("Jest verify failed: %v", vErr),
			"command": vName + " " + strings.Join(vArgs, " "),
			"output":  truncate(string(vOut), 8000),
			"error":   vErr.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap verify jest: %w\n%s", vErr, truncate(string(vOut), 4000))
	}

	changed := []string{relPathForBootstrap(repo, pkgPath), relPathForBootstrap(repo, filepath.Join(pkgDir, "jest.config.cjs")), relPathForBootstrap(repo, filepath.Join(pkgDir, "__tests__", "asqs-bootstrap-smoke.test.cjs"))}
	if isTS {
		changed = append(changed, relPathForBootstrap(repo, filepath.Join(pkgDir, jestGlobalsDTSFile)))
		changed = append(changed, tsconfigPatches...)
	}
	logAudit(audit, ctx, "test_bootstrap.apply_ok", map[string]interface{}{
		"message":         fmt.Sprintf("Jest bootstrap complete (%s); files: package.json, jest.config.cjs, __tests__/asqs-bootstrap-smoke.test.cjs", string(pm)),
		"files_changed":   changed,
		"package_manager": string(pm),
		"stack":           "jest",
		"package_root":    relPathForBootstrap(repo, pkgDir),
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: added Jest (%s); %s install ok; jest --showConfig ok (baseline smoke: __tests__/asqs-bootstrap-smoke.test.cjs)\n", map[bool]string{true: "ts-jest", false: "node"}[isTS], pm)
	return nil
}

func isJSLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	default:
		return false
	}
}
