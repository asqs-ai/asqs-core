package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
)

// Params configures a bootstrap run.
type Params struct {
	// RepoPath is the project workspace directory (absolute): indexer root, often the mono_repo_workspace folder.
	RepoPath string
	// GitRepoRoot is the git repository root used as the Docker bind mount when bootstrap runs in a container.
	// Empty means the same as RepoPath (whole-repo or local runs without a separate root).
	GitRepoRoot string
	// Lang is javascript, typescript, java, or csharp.
	Lang string
	// Config holds enabled, mode (auto|jest|junit|xunit|off), pin_versions, allow_lockfile_change.
	Config *config.TestFrameworkBootstrapConfig
	// RunnerTimeout is runner.timeout from config (e.g. 15m) for install/verify subprocess budget.
	RunnerTimeout string
	// Runner is optional; when set with RunnerType and execution auto/docker, install/verify run in ephemeral Docker for TS/JS and Java.
	Runner *config.RunnerConfig
	// RunnerType is normalized runner.type (e.g. docker, local).
	RunnerType string
	// DockerExtraEnv is appended to ephemeral bootstrap containers (e.g. VSS_NUGET_EXTERNAL_FEED_ENDPOINTS).
	DockerExtraEnv []string
}

// Run performs test framework bootstrap when enabled. Safe to call with nil Auditor (no audit).
func Run(ctx context.Context, p Params, audit Auditor) error {
	cfg := p.Config
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == "auto" {
		mode = "auto"
	}
	if mode == "off" {
		logAudit(audit, ctx, "test_bootstrap.skip", map[string]interface{}{
			"message": "test_framework_bootstrap.mode is off",
			"reason":  "off",
		})
		return nil
	}
	if mode != "auto" && mode != "jest" && mode != "junit" && mode != "xunit" && mode != "off" {
		return fmt.Errorf("test_framework_bootstrap: unsupported mode %q (use auto, jest, junit, xunit, or off)", cfg.Mode)
	}

	repo := filepath.Clean(strings.TrimSpace(p.RepoPath))
	if repo == "" {
		return fmt.Errorf("test_framework_bootstrap: empty repo path")
	}
	var err error
	repo, err = filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	gitRoot := filepath.Clean(strings.TrimSpace(p.GitRepoRoot))
	if gitRoot == "" {
		gitRoot = repo
	} else {
		gitRoot, err = filepath.Abs(gitRoot)
		if err != nil {
			return fmt.Errorf("test_framework_bootstrap: %w", err)
		}
		rel, rerr := filepath.Rel(gitRoot, repo)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("test_framework_bootstrap: workspace %q must be under git root %q", repo, gitRoot)
		}
	}

	lang := strings.ToLower(strings.TrimSpace(p.Lang))
	if mode == "jest" && (lang == "java" || isCSharpLang(lang)) {
		logAudit(audit, ctx, "test_bootstrap.skip_wrong_mode", map[string]interface{}{
			"message": "mode jest does not apply to Java/C#; skipping",
			"lang":    lang,
			"mode":    mode,
		})
		return nil
	}
	if mode == "junit" && (isJSLang(lang) || isCSharpLang(lang)) {
		logAudit(audit, ctx, "test_bootstrap.skip_wrong_mode", map[string]interface{}{
			"message": "mode junit does not apply to JS/TS or C#; skipping",
			"lang":    lang,
			"mode":    mode,
		})
		return nil
	}
	if mode == "xunit" && !isCSharpLang(lang) {
		logAudit(audit, ctx, "test_bootstrap.skip_wrong_mode", map[string]interface{}{
			"message": "mode xunit applies only to C#; skipping",
			"lang":    lang,
			"mode":    mode,
		})
		return nil
	}

	logAudit(audit, ctx, "test_bootstrap.start", map[string]interface{}{
		"message": fmt.Sprintf("Test framework bootstrap: checking %s (%s)", repo, p.Lang),
		"repo":    repo,
		"lang":    p.Lang,
		"mode":    mode,
	})
	fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: checking %s (lang=%s, mode=%s)...\n", repo, p.Lang, mode)

	ed, err := resolveEphemeralDocker(p.Runner, p.RunnerType, strings.TrimSpace(cfg.Execution), lang, gitRoot, repo, p.RunnerTimeout, BootstrapDockerStandard, p.DockerExtraEnv)
	if err != nil {
		return fmt.Errorf("test_framework_bootstrap: %w", err)
	}
	if ed != nil {
		logAudit(audit, ctx, "test_bootstrap.docker", map[string]interface{}{
			"message":   "Install/verify in ephemeral Docker (--rm)",
			"image":     ed.Image(),
			"execution": "docker",
		})
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: ephemeral docker image=%s\n", ed.Image())
	}

	var rep Report
	if isJSLang(lang) {
		// Unit-only detection: Playwright/Cypress alone do not skip Jest bootstrap.
		rep, err = DetectUnit(repo, lang)
	} else {
		rep, err = Detect(repo, lang)
	}
	if err != nil {
		logAuditError(audit, ctx, "test_bootstrap.detect_error", map[string]interface{}{
			"message": fmt.Sprintf("Detection failed: %v", err),
			"error":   err.Error(),
		})
		return fmt.Errorf("test_framework_bootstrap detect: %w", err)
	}
	if rep.HasFramework {
		logAudit(audit, ctx, "test_bootstrap.skip_detected", map[string]interface{}{
			"message":   fmt.Sprintf("Test framework already present (%s): %s", rep.Framework, rep.Reason),
			"framework": rep.Framework,
			"reason":    rep.Reason,
			"has_setup": true,
		})
		fmt.Fprintf(os.Stderr, "  test_framework_bootstrap: skipped — unit test stack already present (%s: %s)\n", rep.Framework, rep.Reason)
		return nil
	}

	switch {
	case isJSLang(lang):
		if err := enforceRequireDockerBootstrap(p.Runner, lang, ed, "test_framework_bootstrap"); err != nil {
			return err
		}
		if mode != "auto" && mode != "jest" {
			return fmt.Errorf("test_framework_bootstrap: mode %q not valid for JS/TS", mode)
		}
		return runJestBootstrap(ctx, repo, cfg, lang, audit, p.RunnerTimeout, ed)
	case lang == "java":
		if err := enforceRequireDockerBootstrap(p.Runner, lang, ed, "test_framework_bootstrap"); err != nil {
			return err
		}
		if mode != "auto" && mode != "junit" {
			return fmt.Errorf("test_framework_bootstrap: mode %q not valid for Java", mode)
		}
		return runJavaBootstrap(ctx, repo, cfg, audit, p.RunnerTimeout, ed)
	case isCSharpLang(lang):
		if err := enforceRequireDockerBootstrap(p.Runner, lang, ed, "test_framework_bootstrap"); err != nil {
			return err
		}
		if mode != "auto" && mode != "xunit" {
			return fmt.Errorf("test_framework_bootstrap: mode %q not valid for C#", mode)
		}
		return runCSharpBootstrap(ctx, repo, gitRoot, cfg, audit, p.RunnerTimeout, ed, p.Runner)
	default:
		logAudit(audit, ctx, "test_bootstrap.skip_lang", map[string]interface{}{
			"message": "Bootstrap apply not implemented for language " + lang,
			"lang":    lang,
		})
		return nil
	}
}

func isCSharpLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		return true
	default:
		return false
	}
}
