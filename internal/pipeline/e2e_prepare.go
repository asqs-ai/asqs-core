package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/testbootstrap"
)

// prepareGeneratedPlaywrightSpecs makes the E2E artifacts a JS/TS Playwright run just produced
// loadable, and re-renders the ASQS-owned playwright.config.ts so it matches them.
//
// Two facts from the run of 2026-09-04 (Angular fixture): the E2E_SPEC gap extended
// `e2e/smoke.e2e-spec.ts` in place and the merged file still opened with the fixture's bare
// `test(` — no `@playwright/test` import — so the whole E2E pass failed at load with
// `ReferenceError: test is not defined` and the first of three post-discard repair rounds bought
// exactly that import line. And the config written at bootstrap cannot know which spec files
// generation will add or extend later; testMatch is derived from the files as they are, so it has
// to be derived again once they exist.
//
// Runs only for JS/TS with Playwright and only over this run's own E2E artifacts. Every step is
// best-effort and audited; nothing here can fail the run.
func prepareGeneratedPlaywrightSpecs(ctx context.Context, repoAbs, lang, e2eFramework string, e2eArtifacts []string, audit runAuditor) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
	default:
		return
	}
	if !strings.EqualFold(strings.TrimSpace(e2eFramework), "playwright") || len(e2eArtifacts) == 0 {
		return
	}
	var imported []string
	for _, rel := range e2eArtifacts {
		full := filepath.Join(repoAbs, filepath.FromSlash(rel))
		if _, serr := os.Stat(full); serr != nil {
			continue // a refused or discarded write; nothing to repair
		}
		wrote, err := testbootstrap.EnsurePlaywrightImport(full)
		if err != nil {
			if audit != nil {
				audit.LogError(ctx, "generate.e2e_playwright_import", map[string]interface{}{
					"message": fmt.Sprintf("Could not check the @playwright/test import of %s: %v", rel, err),
					"path":    rel, "error": err.Error(),
				})
			}
			continue
		}
		if wrote {
			imported = append(imported, rel)
		}
	}
	if len(imported) > 0 && audit != nil {
		audit.Log(ctx, "generate.e2e_playwright_import", map[string]interface{}{
			"message": fmt.Sprintf("Added the missing `import { test, expect } from '@playwright/test'` to %d generated E2E spec(s): %s.", len(imported), strings.Join(imported, ", ")),
			"paths":   imported,
		})
	}
	rel, err := testbootstrap.RefreshPlaywrightConfig(repoAbs, lang)
	if err != nil {
		if audit != nil {
			audit.LogError(ctx, "generate.playwright_config_refresh", map[string]interface{}{
				"message": "Could not re-render the ASQS-owned playwright.config.ts after generation: " + err.Error(),
				"error":   err.Error(),
			})
		}
		return
	}
	if rel != "" && audit != nil {
		audit.Log(ctx, "generate.playwright_config_refresh", map[string]interface{}{
			"message": fmt.Sprintf("Re-rendered %s after generation so testMatch covers the spec files this run added or extended.", rel),
			"path":    rel,
		})
	}
}

// refreshPlaywrightConfigAfterDiscard re-renders the ASQS-owned playwright.config.ts once discarded
// artifacts are off disk, so testMatch describes the specs that remain.
//
// A discard changes the spec set exactly as generation does, and testMatch is derived from that set
// — but nothing re-derived it. Run asqs-go run api-97ae558d51c11770e1fb82310419f5d8 ended there: the survivor
// discard removed the two generated route specs and restored `e2e/smoke.e2e-spec.ts` to the
// repository's own import-less body, while the config kept the `*.e2e-spec.*` pattern that
// extraPlaywrightTestMatch had granted only because that file imported `@playwright/test` at the
// time. The verification then loaded a file that cannot run and failed with "ReferenceError: test
// is not defined", so a tree whose compile, unit tests and surviving smoke spec were green shipped
// nothing.
//
// Only the config is rewritten. EnsurePlaywrightImport is deliberately NOT run here: adding the
// import back to a file the discard just restored would undo the discard, which is the one thing
// this must not do.
func refreshPlaywrightConfigAfterDiscard(ctx context.Context, repoAbs, lang, e2eFramework string, audit runAuditor) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
	default:
		return
	}
	if !strings.EqualFold(strings.TrimSpace(e2eFramework), "playwright") {
		return
	}
	rel, err := testbootstrap.RefreshPlaywrightConfig(repoAbs, lang)
	if err != nil {
		if audit != nil {
			audit.LogError(ctx, "pipeline.post_discard_playwright_config_refresh", map[string]interface{}{
				"message": "Could not re-render the ASQS-owned playwright.config.ts after the discard: " + err.Error(),
				"error":   err.Error(),
			})
		}
		return
	}
	if rel != "" && audit != nil {
		audit.Log(ctx, "pipeline.post_discard_playwright_config_refresh", map[string]interface{}{
			"message": fmt.Sprintf("Re-rendered %s after the discard so testMatch describes the specs still on disk; a discarded or restored spec no longer decides which files the E2E pass loads.", rel),
			"path":    rel,
		})
	}
}
