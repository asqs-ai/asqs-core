package pipeline

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/projectintel"
)

func writeAsqs(t *testing.T, root, rel, body string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Core already commits .asqs/project-intel-cache.json today, and this bundle is the first thing to
// delete anything under .asqs before staging. Dropping that cache would be a silent regression —
// the next run would re-derive project intel from scratch with nothing in the diff to explain why —
// so it is asserted per-path rather than left to the preserve list being "obviously right".
func TestShipCleanup_preservesProjectIntelCache(t *testing.T) {
	root := t.TempDir()
	cache := writeAsqs(t, root, ".asqs/project-intel-cache.json", `{"v":1}`)
	stack := writeAsqs(t, root, ".asqs/test-stack.json", `{"schema_version":1}`)
	scratch := writeAsqs(t, root, ".asqs/scratch/whatever.tmp", "junk")
	outside := writeAsqs(t, root, "src/main.go", "package main")

	RemoveRepoAsqsDirForShip(root, &config.Config{})

	if !exists(cache) {
		t.Error("project-intel-cache.json was removed; that is existing shipped behaviour this bundle must not break")
	}
	if b, err := os.ReadFile(cache); err != nil || string(b) != `{"v":1}` {
		t.Errorf("preserved file content changed: %q, %v", b, err)
	}
	if exists(stack) {
		t.Error("test-stack.json survived: it describes one ephemeral bootstrap and must never be committed")
	}
	if exists(scratch) {
		t.Error("unlisted scratch under .asqs survived")
	}
	if !exists(outside) {
		t.Error("the cleanup reached outside .asqs/")
	}
}

// The web-search cache is what makes CP47's repo-anchored replay reusable at all: it lives inside
// the per-run clone, so if ship drops it every run re-pays for identical queries.
func TestShipCleanup_preservesWebSearchCacheOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		root := t.TempDir()
		cfg := &config.Config{}
		cfg.WebSearch.Enabled = enabled
		cache := writeAsqs(t, root, filepath.ToSlash(cfg.WebSearch.EffectiveCachePath()), "{}")

		RemoveRepoAsqsDirForShip(root, cfg)

		if got := exists(cache); got != enabled {
			t.Errorf("websearch.enabled=%v: cache preserved=%v, want %v", enabled, got, enabled)
		}
	}
}

// The failure hint is preserved on either trigger — persistence turned on, or an explicit path set —
// because both mean an operator expects the file to reach the next run.
func TestShipCleanup_preservesFailureHint(t *testing.T) {
	t.Run("persist flag uses the default path", func(t *testing.T) {
		root := t.TempDir()
		cfg := &config.Config{}
		cfg.Retrieval.PersistLastEvalFailure = true
		hint := writeAsqs(t, root, defaultLastEvalFailureHintRelPath, "boom")
		RemoveRepoAsqsDirForShip(root, cfg)
		if !exists(hint) {
			t.Error("default failure-hint path not preserved with persist_last_eval_failure set")
		}
	})
	t.Run("explicit path alone is enough", func(t *testing.T) {
		root := t.TempDir()
		cfg := &config.Config{}
		cfg.Retrieval.FailureHintFile = ".asqs/my-hint.log"
		hint := writeAsqs(t, root, ".asqs/my-hint.log", "boom")
		RemoveRepoAsqsDirForShip(root, cfg)
		if !exists(hint) {
			t.Error("explicitly configured failure-hint path not preserved")
		}
	})
	t.Run("neither means it is scratch", func(t *testing.T) {
		root := t.TempDir()
		hint := writeAsqs(t, root, defaultLastEvalFailureHintRelPath, "boom")
		RemoveRepoAsqsDirForShip(root, &config.Config{})
		if exists(hint) {
			t.Error("failure hint preserved with neither persistence nor an explicit path")
		}
	})
}

// A preserve entry is operator-controlled (retrieval.failure_hint_file), so it is an untrusted path
// as far as this mechanism is concerned. Escaping .asqs/ must drop the entry, not write outside it.
func TestShipPreserveRelPaths_rejectsEscapes(t *testing.T) {
	for _, bad := range []string{
		"../outside.log",
		".asqs/../../etc/passwd",
		"/etc/passwd",
		"notasqs/hint.log",
		".asqs",
		"",
	} {
		cfg := &config.Config{}
		cfg.Retrieval.FailureHintFile = bad
		cfg.Retrieval.PersistLastEvalFailure = true
		for _, got := range ShipPreserveRelPaths(cfg) {
			if got == bad {
				t.Errorf("preserve list accepted %q", bad)
			}
		}
	}
}

// The escape must be refused at the RESTORE site too, not only when the list is built: the two are
// separate entry points and only the second one actually writes files.
func TestShipCleanup_escapeInPreserveListWritesNothingOutside(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(filepath.Dir(root), "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(victim) })
	writeAsqs(t, root, ".asqs/keep.json", "kept")

	removeRepoAsqsDirPreserving(root, []string{"../victim.txt", ".asqs/../../victim.txt"})

	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Errorf("a path outside the repo root was written: %q", b)
	}
}

// Absence of .asqs entirely, and an empty preserve list, are both ordinary — a repository that never
// ran project intel has neither.
func TestShipCleanup_missingAsqsDirIsNotAnError(t *testing.T) {
	root := t.TempDir()
	RemoveRepoAsqsDirForShip(root, &config.Config{})
	RemoveRepoAsqsDirForShip("", &config.Config{})
	RemoveRepoAsqsDirForShip(root, nil)
}

// Every preserved path must be under .asqs/, since the cleanup deletes nothing else: a preserve
// entry pointing elsewhere is at best a no-op and at worst a write this mechanism should not make.
func TestShipPreserveRelPaths_allUnderAsqs(t *testing.T) {
	cfg := &config.Config{}
	cfg.WebSearch.Enabled = true
	cfg.Retrieval.PersistLastEvalFailure = true
	got := ShipPreserveRelPaths(cfg)
	if len(got) < 3 {
		t.Fatalf("expected project-intel, websearch and failure-hint paths, got %v", got)
	}
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	for _, p := range got {
		if len(p) < len(".asqs/") || p[:len(".asqs/")] != ".asqs/" {
			t.Errorf("preserved path %q is not under .asqs/", p)
		}
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("duplicate preserve entry %q", p)
		}
		seen[p] = true
	}
}

// config and projectintel each name the project-intel cache path, in packages that cannot import
// one another. If they drift, the scan writes one file and the pre-ship preserve list saves a
// different one — so the cache is written, thrown away with the rest of .asqs, and every run starts
// cold, with nothing in the diff to say why. Pinned here because pipeline is the one package that
// sees both.
func TestProjectIntelCachePathHasOneSpelling(t *testing.T) {
	if config.ProjectIntelCachePath != projectintel.DefaultCachePathRel {
		t.Errorf("cache path drifted: config says %q, projectintel says %q",
			config.ProjectIntelCachePath, projectintel.DefaultCachePathRel)
	}
	// And the preserve list must actually name it, or the whole point is lost.
	found := false
	for _, p := range ShipPreserveRelPaths(&config.Config{}) {
		if p == config.ProjectIntelCachePath {
			found = true
		}
	}
	if !found {
		t.Errorf("the preserve list does not name %q; a ship would drop the cache", config.ProjectIntelCachePath)
	}
}
