package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/pathsafe"
)

// defaultLastEvalFailureHintRelPath is where persist_last_eval_failure writes when
// retrieval.failure_hint_file names no path of its own.
const defaultLastEvalFailureHintRelPath = ".asqs/last-eval-failure.log"

// maxPersistedFailureHintBytes bounds the persisted hint. Test output is unbounded — a flaky suite
// can emit megabytes — and this file is read back into a PROMPT next run, so an unbounded write is
// an unbounded prompt.
const maxPersistedFailureHintBytes = 512 * 1024

// failureHintReadRelPath is the repo-relative path planning reads a hint from, or "" for none.
//
// An explicit retrieval.failure_hint_file always wins: it is how a CI job points the planner at a
// build log it already has. Otherwise the default path is read only when persistence wrote it, so
// a stale file left in a workspace cannot silently steer a run nobody configured for it.
func failureHintReadRelPath(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if s := strings.TrimSpace(cfg.Retrieval.FailureHintFile); s != "" {
		return filepath.ToSlash(s)
	}
	if cfg.Retrieval.PersistLastEvalFailure {
		return defaultLastEvalFailureHintRelPath
	}
	return ""
}

// failureHintWriteRelPath is where persistence writes. Unlike the read side it always resolves to a
// path, because persist_last_eval_failure being on is itself the instruction to write one.
func failureHintWriteRelPath(cfg *config.Config) string {
	if cfg == nil {
		return defaultLastEvalFailureHintRelPath
	}
	if s := strings.TrimSpace(cfg.Retrieval.FailureHintFile); s != "" {
		return filepath.ToSlash(s)
	}
	return defaultLastEvalFailureHintRelPath
}

// loadFailureHintFromRepoFile reads the hint file, returning "" when there is nothing usable.
//
// Every failure mode is the same answer — plan without a hint — so this reports no error: the hint
// is an optimisation, and a run must not fail because a log file an operator mentioned is missing.
func loadFailureHintFromRepoFile(repoPath, rel string) string {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(rel) == "" {
		return ""
	}
	clean, ok := pathsafe.ContainedRelPath(rel, repoPath)
	if !ok {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(clean)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// evalFailureHintFromSteps renders the failing compile/test/e2e steps as the next run's hint.
//
// Only those three kinds are included. Lint, coverage and mutation failures say nothing about which
// production code the tests got wrong, which is the only question the hint is used to answer.
func evalFailureHintFromSteps(results []evaluator.StepResult) string {
	var b strings.Builder
	for _, sr := range results {
		if sr.OK {
			continue
		}
		switch sr.Step {
		case evaluator.StepCompile, evaluator.StepTest, evaluator.StepTestE2E:
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString("[")
			b.WriteString(string(sr.Step))
			b.WriteString("] ")
			b.WriteString(strings.TrimSpace(sr.Summary))
			b.WriteString("\n\n")
			if out := strings.TrimSpace(sr.Output); out != "" {
				b.WriteString(out)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// persistLastEvalFailureHint closes the loop retrieval.failure_hint_file opens: this run's failing
// output becomes the next run's planning hint, so a repository that keeps failing the same way gets
// retrieval steered at the failure instead of starting cold every time.
//
// A STABLE run removes the file. Leaving it would hand the next run a hint describing a failure that
// no longer exists, which is worse than no hint — retrieval would localise on code that is already
// fixed. Best-effort throughout: this runs after the deliverable is produced, and no write failure
// here is worth failing a run over.
func persistLastEvalFailureHint(repoPath string, cfg *config.Config, stable bool, steps []evaluator.StepResult) {
	if cfg == nil || !cfg.Retrieval.PersistLastEvalFailure || strings.TrimSpace(repoPath) == "" {
		return
	}
	rel, ok := pathsafe.ContainedRelPath(failureHintWriteRelPath(cfg), repoPath)
	if !ok {
		return
	}
	full := filepath.Join(repoPath, filepath.FromSlash(rel))
	if stable {
		_ = os.Remove(full)
		return
	}
	text := evalFailureHintFromSteps(steps)
	if text == "" {
		return
	}
	if len(text) > maxPersistedFailureHintBytes {
		text = text[:maxPersistedFailureHintBytes] + "\n... [truncated by persist_last_eval_failure]"
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(full, []byte(text), 0o644)
}
