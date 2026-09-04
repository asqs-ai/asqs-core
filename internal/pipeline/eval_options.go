package pipeline

import (
	"context"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/testbootstrap"
)

// detectRunE2EFramework names the E2E stack the repository carries AFTER the E2E bootstrap has had
// its chance to install one, for the languages the evaluator runs a dual (unit + E2E) pass on.
//
// This value used to exist only inside the bootstrap. The evaluator's EvalOptions.E2EFramework was
// never set by this pipeline, so resolveE2ETestCommand fell to its `npm run test:e2e` fallback and
// usePlaywrightDockerForJSE2E answered false: the E2E pass of the run of 2026-09-03 executed in
// node:22-bookworm and Playwright looked for browsers under /root/.cache/ms-playwright — the
// Playwright image, which the runner would have swapped in for "playwright", was never selected.
// asqs-go has always derived this from testbootstrap.DetectE2E after bootstrap; this is that.
func detectRunE2EFramework(ctx context.Context, repoAbs, lang string, audit runAuditor) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts", "java", "csharp", "cs":
	default:
		return ""
	}
	rep, err := testbootstrap.DetectE2E(repoAbs, lang)
	fw := ""
	if err == nil && rep.HasE2E {
		fw = strings.TrimSpace(rep.Framework)
	}
	if audit != nil {
		payload := map[string]interface{}{
			"framework": fw,
			"detected":  fw != "",
			"reason":    rep.Reason,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		if fw != "" {
			payload["message"] = "E2E framework for the evaluation pass: " + fw + " (" + rep.Reason + ")."
		} else {
			payload["message"] = "No E2E framework detected for the evaluation pass; a generated E2E artifact would run in the plain toolchain image with the package.json test:e2e script."
		}
		audit.Log(ctx, "pipeline.e2e_framework", payload)
	}
	return fw
}

// evalOptionsFromConfig returns the EvalOptions fields that come from configuration and the run's
// E2E detection — everything the evaluator needs that is not a path or an artifact list.
//
// One place, so a config key declared for the evaluator cannot be translated and then dropped on
// the way in. Two were, and both cost the run of 2026-09-03: E2EFramework (above) and
// SkipFixerOnInfrastructureFailure, which fixer.policy.skip_on_infrastructure_failure sets in
// config.Runner but which never reached EvalOptions — so a `browsers_missing` classification was
// audited and then handed to the fixer for two of the three post-discard repair rounds anyway.
func evalOptionsFromConfig(cfg *config.Config, e2eFramework string, anyE2E bool) evaluator.EvalOptions {
	opts := evaluator.EvalOptions{
		// Per-step repair budgets, independent of the iteration budget. Unset = the iteration budget.
		MaxCompileFixAttempts: cfg.Runner.MaxCompileFixAttempts,
		MaxTestFixAttempts:    cfg.Runner.MaxTestFixAttempts,
		// Keep the evaluator's view of the flag in step with the Fixer's, or the audit payload
		// (structured_user_message_config / _forced) contradicts what the fixer actually did.
		FixerStructuredUserMessage:       cfg.Runner.FixerStructuredUserMessage,
		RunE2ETestPass:                   anyE2E,
		E2EFramework:                     strings.TrimSpace(e2eFramework),
		E2ETestCommand:                   strings.TrimSpace(cfg.Runner.E2ETestCommand),
		SkipFixerOnInfrastructureFailure: cfg.Runner.SkipFixerOnInfrastructureFailure,
		CompileOncePerEval:               true,
		// Fix-loop bounds. The breaker thresholds were hardcoded, leaving an operator watching a
		// loop give up after three rounds with no lever at all.
		FixLoopRepeatStopThreshold:     cfg.Runner.FixLoopRepeatStopThreshold,
		FixLoopRecurrenceStopThreshold: cfg.Runner.FixLoopRecurrenceStopThreshold,
		FixLoopNoProgressStopThreshold: cfg.Runner.FixLoopNoProgressStopThreshold,
		FixContextRunesMax:             cfg.Runner.FixContextRunesMax,
		BackoffBetweenFixAttempts:      fixBackoffDuration(cfg.Runner.FixBackoff),
		// runner.disable_error_log_llm_summary was declared and documented but never passed on, so
		// the summariser could not be turned off — another key the inert-field lint could not see,
		// because EvalOptions declares an identically named field.
		DisableErrorLogLLMSummary: cfg.Runner.DisableErrorLogLLMSummary,
	}
	return opts
}
