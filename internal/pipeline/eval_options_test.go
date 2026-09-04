package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

// Run of 2026-09-03: the evaluator never learned the E2E framework, so the E2E pass used the
// `npm run test:e2e` fallback in the plain Node image and Playwright found no browsers.
func TestDetectRunE2EFramework_readsPlaywrightFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","devDependencies":{"@playwright/test":"1.49.1"},"scripts":{"test:e2e":"playwright test"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &payloadAuditor{}
	if got := detectRunE2EFramework(context.Background(), dir, "typescript", a); got != "playwright" {
		t.Fatalf("framework = %q, want playwright", got)
	}
	p := a.lastPayload("pipeline.e2e_framework")
	if p == nil || p["framework"] != "playwright" || p["detected"] != true {
		t.Fatalf("expected an audited pipeline.e2e_framework with framework=playwright; got %v", p)
	}

	none := t.TempDir()
	if err := os.WriteFile(filepath.Join(none, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a2 := &payloadAuditor{}
	if got := detectRunE2EFramework(context.Background(), none, "typescript", a2); got != "" {
		t.Fatalf("framework = %q, want \"\" without an E2E dependency", got)
	}
	if p := a2.lastPayload("pipeline.e2e_framework"); p == nil || p["detected"] != false {
		t.Fatalf("the absence must be audited too; got %v", p)
	}
	if got := detectRunE2EFramework(context.Background(), dir, "go", nil); got != "" {
		t.Fatalf("framework = %q, want \"\" for a language without a dual E2E pass", got)
	}
}

// Config keys the evaluator declares must arrive in EvalOptions. Two did not before this: the E2E
// framework and fixer.policy.skip_on_infrastructure_failure, which was translated into
// config.Runner and then never read.
func TestEvalOptionsFromConfig_propagatesE2EAndInfrastructureSkip(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runner.SkipFixerOnInfrastructureFailure = true
	cfg.Runner.E2ETestCommand = "npx playwright test --project=chromium"
	cfg.Runner.FixLoopNoProgressStopThreshold = 7

	opts := evalOptionsFromConfig(cfg, " playwright ", true)
	if opts.E2EFramework != "playwright" {
		t.Errorf("E2EFramework = %q, want playwright (trimmed)", opts.E2EFramework)
	}
	if opts.E2ETestCommand != "npx playwright test --project=chromium" {
		t.Errorf("E2ETestCommand = %q, want the configured command", opts.E2ETestCommand)
	}
	if !opts.SkipFixerOnInfrastructureFailure {
		t.Error("SkipFixerOnInfrastructureFailure must follow config.Runner")
	}
	if !opts.RunE2ETestPass {
		t.Error("RunE2ETestPass must be on when an E2E artifact was generated")
	}
	if opts.FixLoopNoProgressStopThreshold != 7 || !opts.CompileOncePerEval {
		t.Errorf("other config-derived fields lost: noProgress=%d compileOnce=%v", opts.FixLoopNoProgressStopThreshold, opts.CompileOncePerEval)
	}

	off := evalOptionsFromConfig(&config.Config{}, "", false)
	if off.RunE2ETestPass || off.E2EFramework != "" || off.SkipFixerOnInfrastructureFailure {
		t.Errorf("defaults must stay off: %+v", off)
	}
}
