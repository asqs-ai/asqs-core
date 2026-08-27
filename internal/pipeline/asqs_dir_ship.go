package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/pathsafe"
)

// ShipPreserveRelPaths returns the repo-relative slash paths under .asqs/ that must survive the
// pre-ship cleanup.
//
// It is a PRESERVE-list, not a purge, and that is the whole design. Everything named here is
// committed into the repository under test, so it reaches the NEXT run through that repository's
// clone — which is the only way a cache written inside a per-run workspace survives at all. The
// contract goes the other way: .asqs/test-stack.json describes one bootstrap of one environment, so
// committing it would hand a later run a stale allow-list that reads as authoritative.
//
// Anything not under .asqs/ is deliberately rejected rather than preserved: the cleanup only touches
// .asqs, so a file elsewhere in the tree is already staged by `git add .` and needs no help.
func ShipPreserveRelPaths(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(rel string) {
		rel, ok := pathsafe.ContainedRelPath(rel, "")
		if !ok {
			return
		}
		// Under .asqs/ specifically — containment in the repo root is necessary but not sufficient,
		// since preserving an arbitrary repo path would let a config key reach outside this
		// mechanism's remit.
		if rel == ".asqs" || !strings.HasPrefix(rel, ".asqs/") {
			return
		}
		if _, dup := seen[rel]; dup {
			return
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}

	pi := cfg.EffectiveProjectIntel()
	if pi.EffectiveEnabled() && pi.EffectiveCacheEnabled() {
		add(pi.EffectiveCachePath())
	}
	// The web-search replay cache, on exactly the same terms as the project-intel cache: a
	// repo-scoped artifact of the repository under test, so it belongs in that repository. Without
	// it the cache could not survive a run at all — written inside the per-run clone, then deleted
	// with .asqs before `git add .`, so every run re-paid for identical queries.
	if cfg.WebSearch.Enabled {
		add(cfg.WebSearch.EffectiveCachePath())
	}
	// The failure hint is the one preserved path an operator can move, so it is also the only one
	// where the containment check above can actually bite.
	if cfg.Retrieval.PersistLastEvalFailure {
		if s := strings.TrimSpace(cfg.Retrieval.FailureHintFile); s != "" {
			add(s)
		} else {
			add(defaultLastEvalFailureHintRelPath)
		}
	} else if s := strings.TrimSpace(cfg.Retrieval.FailureHintFile); s != "" {
		add(s)
	}
	return out
}

// defaultLastEvalFailureHintRelPath is where persist_last_eval_failure writes when
// retrieval.failure_hint_file names no path of its own.
const defaultLastEvalFailureHintRelPath = ".asqs/last-eval-failure.log"

// RemoveRepoAsqsDirForShip deletes repoPath/.asqs before staging, restoring the preserved files.
//
// Called immediately before `git add .`: without it a ship commits .asqs/test-stack.json, which
// describes the bootstrap of one ephemeral environment and would be read by later runs as an
// authoritative statement about a repository it no longer matches.
//
// Best-effort throughout. A cleanup that fails must not fail the ship — the run's actual deliverable
// is the generated tests, and refusing to commit them because a cache file could not be re-read
// would trade the whole run for a tidiness problem.
func RemoveRepoAsqsDirForShip(repoPath string, cfg *config.Config) {
	removeRepoAsqsDirPreserving(repoPath, ShipPreserveRelPaths(cfg))
}

// removeRepoAsqsDirPreserving deletes repoPath/.asqs except the listed repo-relative slash paths.
// Only regular files under .asqs/ are restored; unsafe or unreadable paths are skipped.
func removeRepoAsqsDirPreserving(repoPath string, preserveRepoRel []string) {
	if strings.TrimSpace(repoPath) == "" {
		return
	}
	root := filepath.Clean(repoPath)
	target := filepath.Join(root, ".asqs")
	if _, ok := pathsafe.ContainedRelPath(target, root); !ok {
		return
	}
	if len(preserveRepoRel) == 0 {
		_ = os.RemoveAll(target)
		return
	}
	// Read before the delete, write after: .asqs is removed wholesale rather than walked, because a
	// walk-and-skip would have to reason about directories it half-emptied.
	saved := make(map[string][]byte, len(preserveRepoRel))
	for _, rel := range preserveRepoRel {
		clean, ok := pathsafe.ContainedRelPath(rel, root)
		if !ok || (clean != ".asqs" && !strings.HasPrefix(clean, ".asqs/")) {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(clean))
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		saved[clean] = b
	}
	_ = os.RemoveAll(target)
	for rel, b := range saved {
		dest := filepath.Join(root, filepath.FromSlash(rel))
		if _, ok := pathsafe.ContainedRelPath(dest, root); !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			continue
		}
		_ = os.WriteFile(dest, b, 0o644)
	}
}
