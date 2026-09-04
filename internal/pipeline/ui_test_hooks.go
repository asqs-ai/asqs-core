package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/uitesthooks"
)

// uiTestHooksOutcome is what the run records about the test-id pass.
type uiTestHooksOutcome struct {
	// Files that carry new attributes after the pass (rolled-back files are not listed).
	Files []string
	// Added is the number of attributes that survived verification.
	Added int
	// RolledBack is set when the compile check failed and every write was restored.
	RolledBack bool
}

// uiTestHooksOptions maps the resolved configuration onto the package's options.
func uiTestHooksOptions(cfg *config.Config) uitesthooks.Options {
	if cfg == nil {
		return uitesthooks.Options{}
	}
	return uitesthooks.Options{
		Enabled:    cfg.Generation.UITestHooksEnabled,
		MaxFiles:   cfg.Generation.UITestHooksMaxFiles,
		MaxPerFile: cfg.Generation.UITestHooksMaxPerFile,
		Templates:  cfg.Generation.UITestHooksTemplates,
	}
}

// uiTestHooksVendoredDir is the js-ts-indexer's package root; its node_modules is the fallback
// typescript for a repository that does not vendor one (asqs-go's seam uses its own testdata copy).
func uiTestHooksVendoredDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "tools", "js-ts-indexer")
}

// applyUITestHooks adds data-testid attributes to the repository's UI sources before the index
// step when generation.policy.ui_test_hooks is enabled, so the JSX/HTML hook enrichers see them
// and the E2E generator's selector inventory is not empty on a repository that ships without
// test ids. asqs-core has no seam phase, so this is the pipeline's own guarded version of what
// asqs-go runs inside the seam: every write is journalled first, the tree is compiled in the
// sandbox afterwards, and a tree that stops compiling is restored byte-for-byte. A nil sandbox
// skips verification (the local runner is created before this is called; nil only happens in
// tests), which is audited.
func applyUITestHooks(ctx context.Context, cfg *config.Config, repoAbs, lang string, sandbox evaluator.SandboxRunner, audit runAuditor) uiTestHooksOutcome {
	var out uiTestHooksOutcome
	opts := uiTestHooksOptions(cfg)
	if !opts.Enabled || strings.TrimSpace(repoAbs) == "" {
		return out
	}
	plan := uitesthooks.PlanTargets(repoAbs, opts)
	if len(plan.Targets) == 0 {
		audit.Log(ctx, "ui_test_hooks.summary", map[string]interface{}{
			"message":       "UI test hooks pass found no eligible UI source files.",
			"files_planned": 0, "files_changed": 0, "hooks_added": 0, "skipped": plan.Skipped,
		})
		return out
	}
	journal := uitesthooks.NewJournal()
	res, err := uitesthooks.Apply(ctx, repoAbs, plan, opts, journal.Snapshot, uiTestHooksVendoredDir())
	if err != nil {
		audit.LogError(ctx, "ui_test_hooks.error", map[string]interface{}{
			"message": fmt.Sprintf("UI test hooks pass failed before writing: %v. The run continues without added hooks.", err),
			"error":   err.Error(),
		})
		return out
	}
	for _, a := range res.Applied {
		names := make([]string, 0, len(a.Added))
		for _, h := range a.Added {
			names = append(names, h.Name)
		}
		audit.Log(ctx, "ui_test_hooks.applied", map[string]interface{}{
			"message": fmt.Sprintf("Added %d data-testid attribute(s) to %s (%d element(s) already hooked or spread were left alone).", len(a.Added), a.Rel, a.Skipped),
			"file":    filepath.ToSlash(a.Rel),
			"kind":    a.Kind,
			"hooks":   names,
			"skipped": a.Skipped,
		})
	}
	skipped := plan.Skipped
	for _, f := range res.Failed {
		skipped[f.Rel] = "inserter_refused: " + f.Error
	}
	if len(res.Applied) > 0 {
		if sandbox == nil {
			audit.Log(ctx, "ui_test_hooks.verify_skipped", map[string]interface{}{
				"message": "No sandbox runner available; the test-id edits were not compile-checked.",
			})
		} else {
			step := evaluator.RunCompile(ctx, sandbox, evaluator.EvalOptions{RepoPath: repoAbs, Lang: lang, CompileCommand: cfg.Runner.CompileCommand})
			if !step.OK {
				restored := journal.RestoreAll()
				out.RolledBack = true
				audit.LogError(ctx, "ui_test_hooks.rolled_back", map[string]interface{}{
					"message":        "UI test hooks pass reverted: the repository no longer compiled after the attributes were added. The run continues against the original source.",
					"restored_files": restored,
					"compile_output": step.Output,
				})
				return out
			}
		}
	}
	out.Files = res.ChangedRels()
	out.Added = res.TotalAdded
	sort.Strings(out.Files)
	audit.Log(ctx, "ui_test_hooks.summary", map[string]interface{}{
		"message": fmt.Sprintf("UI test hooks pass: %d file(s) planned, %d changed, %d attribute(s) added, %d unchanged, %d refused.",
			len(plan.Targets), len(res.Applied), res.TotalAdded, len(res.Unchanged), len(res.Failed)),
		"files_planned": len(plan.Targets),
		"files_changed": len(res.Applied),
		"files":         out.Files,
		"hooks_added":   res.TotalAdded,
		"unchanged":     len(res.Unchanged),
		"skipped":       skipped,
		"max_files":     opts.Normalized().MaxFiles,
		"max_per_file":  opts.Normalized().MaxPerFile,
	})
	return out
}
