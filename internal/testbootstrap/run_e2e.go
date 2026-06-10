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

// E2EParams configures E2E (Playwright/Cypress) bootstrap when indexer.max_gaps_e2e > 0.
type E2EParams struct {
	RepoPath       string
	GitRepoRoot    string
	Lang           string
	Config         *config.E2EFrameworkBootstrapConfig
	MaxGapsE2E     int
	RunnerTimeout  string
	Runner         *config.RunnerConfig
	RunnerType     string
	DockerExtraEnv []string
}

// RunE2EBootstrap installs Playwright or Cypress when enabled, gaps > 0, and no E2E stack detected.
// JS/TS: npm/yarn Playwright or Cypress. Java: Maven/Gradle + Playwright Java. C#: NuGet Microsoft.Playwright + xUnit smoke test.
func RunE2EBootstrap(ctx context.Context, p E2EParams, audit Auditor) error {
	cfg := p.Config
	if cfg == nil || !cfg.Enabled || p.MaxGapsE2E <= 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == "auto" {
		mode = "auto"
	}
	if mode == "off" {
		logAudit(audit, ctx, "e2e_bootstrap.skip", map[string]interface{}{
			"message": "e2e_framework_bootstrap.mode is off",
			"reason":  "off",
		})
		fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: skipped — mode is off")
		return nil
	}
	if mode != "auto" && mode != "playwright" && mode != "cypress" {
		return fmt.Errorf("e2e_framework_bootstrap: unsupported mode %q (use auto, playwright, cypress, or off)", cfg.Mode)
	}

	repo := filepath.Clean(strings.TrimSpace(p.RepoPath))
	if repo == "" {
		return fmt.Errorf("e2e_framework_bootstrap: empty repo path")
	}
	var err error
	repo, err = filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: %w", err)
	}
	gitRoot := filepath.Clean(strings.TrimSpace(p.GitRepoRoot))
	if gitRoot == "" {
		gitRoot = repo
	} else {
		gitRoot, err = filepath.Abs(gitRoot)
		if err != nil {
			return fmt.Errorf("e2e_framework_bootstrap: %w", err)
		}
		rel, rerr := filepath.Rel(gitRoot, repo)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("e2e_framework_bootstrap: workspace %q must be under git root %q", repo, gitRoot)
		}
	}
	lang := strings.ToLower(strings.TrimSpace(p.Lang))

	logAudit(audit, ctx, "e2e_bootstrap.start", map[string]interface{}{
		"message":      fmt.Sprintf("E2E framework bootstrap: checking %s (%s)", repo, p.Lang),
		"repo":         repo,
		"lang":         p.Lang,
		"mode":         mode,
		"max_gaps_e2e": p.MaxGapsE2E,
	})
	fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: checking %s (lang=%s, mode=%s, max_gaps_e2e=%d)...\n", repo, p.Lang, mode, p.MaxGapsE2E)

	rep, err := DetectE2E(repo, lang)
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.detect_error", map[string]interface{}{
			"message": fmt.Sprintf("E2E detection failed: %v", err),
			"error":   err.Error(),
		})
		fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: detection failed: %v\n", err)
		return fmt.Errorf("e2e_framework_bootstrap detect: %w", err)
	}
	if rep.HasE2E {
		logAudit(audit, ctx, "e2e_bootstrap.skip_detected", map[string]interface{}{
			"message":   fmt.Sprintf("E2E stack already present (%s): %s", rep.Framework, rep.Reason),
			"framework": rep.Framework,
			"reason":    rep.Reason,
			"has_setup": true,
		})
		fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: skipped — E2E stack already present (%s: %s)\n", rep.Framework, rep.Reason)
		return nil
	}

	dockerKind := BootstrapDockerStandard
	switch {
	case isJSLang(lang):
		dockerKind = BootstrapDockerPlaywrightJS
	case lang == "java":
		dockerKind = BootstrapDockerPlaywrightJava
	case isCSharpLang(lang):
		dockerKind = BootstrapDockerPlaywrightDotNet
	}
	ed, err := resolveEphemeralDocker(p.Runner, p.RunnerType, strings.TrimSpace(cfg.Execution), lang, gitRoot, repo, p.RunnerTimeout, dockerKind, p.DockerExtraEnv)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: %w", err)
	}
	if ed != nil {
		dockerPayload := map[string]interface{}{
			"message":   "Install/verify in ephemeral Docker (--rm)",
			"image":     ed.Image(),
			"execution": "docker",
		}
		switch dockerKind {
		case BootstrapDockerPlaywrightJS:
			dockerPayload["js_stack"] = "mcr.microsoft.com/playwright"
		case BootstrapDockerPlaywrightJava:
			dockerPayload["java_stack"] = "mcr.microsoft.com/playwright/java"
		case BootstrapDockerPlaywrightDotNet:
			dockerPayload["dotnet_stack"] = "mcr.microsoft.com/playwright/dotnet"
		}
		logAudit(audit, ctx, "e2e_bootstrap.docker", dockerPayload)
		fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: ephemeral docker image=%s\n", ed.Image())
	}

	if err := enforceRequireDockerBootstrap(p.Runner, lang, ed, "e2e_framework_bootstrap"); err != nil {
		return err
	}

	if isJSLang(lang) {
		if mode == "cypress" {
			return applyCypressBootstrap(ctx, p, audit, repo, cfg, ed)
		}
		return applyPlaywrightBootstrap(ctx, p, audit, repo, cfg, ed)
	}

	if lang == "java" {
		if mode == "cypress" {
			logAudit(audit, ctx, "e2e_bootstrap.mode_fallback", map[string]interface{}{
				"message": "e2e_framework_bootstrap.mode=cypress is not supported for Java; using Playwright Java",
				"from":    "cypress", "to": "playwright-java",
			})
			fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: mode cypress → playwright-java (Java)")
		}
		return applyPlaywrightJavaBootstrap(ctx, p, audit, repo, ed)
	}

	if isCSharpLang(lang) {
		if mode == "cypress" {
			logAudit(audit, ctx, "e2e_bootstrap.mode_fallback", map[string]interface{}{
				"message": "e2e_framework_bootstrap.mode=cypress is not supported for C#; using Playwright .NET",
				"from":    "cypress", "to": "playwright-dotnet",
			})
			fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: mode cypress → playwright-dotnet (C#)")
		}
		return applyPlaywrightDotNetBootstrap(ctx, p, audit, repo, gitRoot, ed)
	}

	logAudit(audit, ctx, "e2e_bootstrap.skip_apply", map[string]interface{}{
		"message": "E2E framework bootstrap does not apply to this language; set runner.e2e_test_command if needed",
		"lang":    lang,
	})
	fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: skipped apply for lang=%s\n", lang)
	return nil
}

func applyPlaywrightBootstrap(ctx context.Context, p E2EParams, audit Auditor, repo string, cfg *config.E2EFrameworkBootstrapConfig, ed *EphemeralDocker) error {
	logAudit(audit, ctx, "e2e_bootstrap.apply_start", map[string]interface{}{
		"message": "Installing default Playwright stack (@playwright/test)",
		"stack":   "playwright",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: installing Playwright (@playwright/test, playwright.config.ts, e2e/)…")

	pkgDir, err := resolveJSPackageDirForBootstrap(repo)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: %w", err)
	}
	pkgPath := filepath.Join(pkgDir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: package.json required: %w", err)
	}
	if err := mergePlaywrightIntoPackageJSON(pkgPath, cfg.PinVersions); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to update package.json: %v", err),
			"step":    "merge_package_json",
			"error":   err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap package.json: %w", err)
	}
	if err := writePlaywrightConfig(pkgDir); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write playwright.config.ts: %v", err),
			"error":   err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap playwright config: %w", err)
	}
	if err := writePlaywrightSmokeSpec(pkgDir); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write e2e/smoke.spec.ts: %v", err),
			"step":    "playwright_smoke_spec",
			"error":   err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap playwright smoke spec: %w", err)
	}

	npmWorkdir := npmInstallWorkdir(repo, pkgDir)
	if err := e2eInstallAndVerify(ctx, p, audit, repo, npmWorkdir, cfg, ed, "playwright", func(vCtx context.Context) error {
		vName, vArgs := playwrightVerifyArgs()
		vArgv := append([]string{vName}, vArgs...)
		vOut, vErr := RunArgv(vCtx, ed, npmWorkdir, vArgv, []string{"CI=true", "NPM_CONFIG_YES=true"})
		if vErr != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
				"message": fmt.Sprintf("Playwright verify failed: %v", vErr),
				"command": vName + " " + strings.Join(vArgs, " "),
				"output":  truncate(string(vOut), 8000),
				"error":   vErr.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap verify playwright: %w\n%s", vErr, truncate(string(vOut), 4000))
		}
		return nil
	}); err != nil {
		return err
	}

	pm := detectPackageManager(npmWorkdir)
	logAudit(audit, ctx, "e2e_bootstrap.apply_ok", map[string]interface{}{
		"message":         fmt.Sprintf("Playwright bootstrap complete (%s); package.json, playwright.config.ts, e2e/smoke.spec.ts", string(pm)),
		"files_changed":   []string{"package.json", "playwright.config.ts", "e2e/smoke.spec.ts"},
		"package_manager": string(pm),
		"stack":           "playwright",
		"package_root":    relPathForBootstrap(repo, pkgDir),
	})
	fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: added Playwright; %s install ok; playwright test --list ok\n", pm)
	return nil
}

func applyCypressBootstrap(ctx context.Context, p E2EParams, audit Auditor, repo string, cfg *config.E2EFrameworkBootstrapConfig, ed *EphemeralDocker) error {
	logAudit(audit, ctx, "e2e_bootstrap.apply_start", map[string]interface{}{
		"message": "Installing default Cypress stack (cypress devDependency)",
		"stack":   "cypress",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: installing Cypress (cypress, cypress.config.ts, cypress/e2e/)…")

	pkgDir, err := resolveJSPackageDirForBootstrap(repo)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: %w", err)
	}
	pkgPath := filepath.Join(pkgDir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: package.json required: %w", err)
	}
	if err := mergeCypressIntoPackageJSON(pkgPath, cfg.PinVersions); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to update package.json: %v", err),
			"step":    "merge_package_json_cypress",
			"error":   err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap package.json: %w", err)
	}
	if err := writeCypressConfig(pkgDir); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to write cypress.config.ts: %v", err),
			"error":   err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap cypress config: %w", err)
	}
	if err := writeCypressSmokeSpec(pkgDir); err != nil {
		return fmt.Errorf("e2e_framework_bootstrap cypress spec: %w", err)
	}

	npmWorkdir := npmInstallWorkdir(repo, pkgDir)
	if err := e2eInstallAndVerify(ctx, p, audit, repo, npmWorkdir, cfg, ed, "cypress", func(vCtx context.Context) error {
		vArgv := []string{"npx", "--yes", "cypress", "verify"}
		vOut, vErr := RunArgv(vCtx, ed, npmWorkdir, vArgv, []string{"CI=true", "NPM_CONFIG_YES=true"})
		if vErr != nil {
			logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
				"message": fmt.Sprintf("Cypress verify failed: %v", vErr),
				"command": "npx --yes cypress verify",
				"output":  truncate(string(vOut), 8000),
				"error":   vErr.Error(),
			})
			return fmt.Errorf("e2e_framework_bootstrap verify cypress: %w\n%s", vErr, truncate(string(vOut), 4000))
		}
		return nil
	}); err != nil {
		return err
	}

	pm := detectPackageManager(npmWorkdir)
	logAudit(audit, ctx, "e2e_bootstrap.apply_ok", map[string]interface{}{
		"message":         fmt.Sprintf("Cypress bootstrap complete (%s); package.json, cypress.config.ts, cypress/e2e/", string(pm)),
		"files_changed":   []string{"package.json", "cypress.config.ts", "cypress/e2e/smoke.cy.ts"},
		"package_manager": string(pm),
		"stack":           "cypress",
		"package_root":    relPathForBootstrap(repo, pkgDir),
	})
	fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: added Cypress; %s install ok; cypress verify ok\n", pm)
	return nil
}

func e2eInstallAndVerify(ctx context.Context, p E2EParams, audit Auditor, repoRoot, installWorkdir string, cfg *config.E2EFrameworkBootstrapConfig, ed *EphemeralDocker, stack string, verify func(context.Context) error) error {
	pm := detectPackageManager(installWorkdir)
	hasLock := hasLockfile(installWorkdir, pm)
	pnpmStore := ""
	if pm == PMPnpm {
		if err := EnsurePnpmBootstrapGitignore(repoRoot); err != nil {
			return fmt.Errorf("e2e_framework_bootstrap .gitignore: %w", err)
		}
		var err error
		pnpmStore, err = BootstrapPnpmStorePath(ed != nil)
		if err != nil {
			return fmt.Errorf("e2e_framework_bootstrap pnpm store path: %w", err)
		}
	}
	// package.json was merged with Playwright/Cypress; lockfile must refresh (pnpm: CI=true implies frozen).
	cmdLine := installCmdLine(pm, cfg.AllowLockfileChange, hasLock, true, pnpmStore)

	timeout := installTimeout(p.RunnerTimeout)
	instCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	installMsg := fmt.Sprintf("Running %s", cmdLine)
	if ed != nil {
		installMsg = fmt.Sprintf("Running %s (docker: corepack enable; then install)", cmdLine)
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": installMsg,
		"command": cmdLine,
		"pm":      string(pm),
		"stack":   stack,
	})

	out, err := RunPackageManagerInstall(instCtx, ed, installWorkdir, pm, cfg.AllowLockfileChange, hasLock, true, []string{"CI=true", "NPM_CONFIG_YES=true"})
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.install_failed", map[string]interface{}{
			"message": fmt.Sprintf("Install failed: %v", err),
			"output":  truncate(string(out), 8000),
			"error":   err.Error(),
			"stack":   stack,
		})
		return fmt.Errorf("e2e_framework_bootstrap install: %w\n%s", err, truncate(string(out), 4000))
	}

	vCtx, vCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer vCancel()
	return verify(vCtx)
}

func playwrightVerifyArgs() (string, []string) {
	return "npx", []string{"--yes", "playwright", "test", "--list"}
}
