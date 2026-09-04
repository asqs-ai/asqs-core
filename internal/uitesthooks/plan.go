package uitesthooks

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Target is one file the pass will edit.
type Target struct {
	// Rel is the repo-relative, forward-slashed path.
	Rel string
	// Kind is "jsx" for .tsx/.jsx sources and "html" for Angular component templates.
	Kind string
}

// Plan lists the files the pass may touch, in a stable order, with the reasons it declined others.
type Plan struct {
	Targets []Target
	// Skipped maps a repo-relative path to why it was left alone; only paths that LOOKED like
	// candidates are listed, so a reviewer can see what the caps and the snapshot rule excluded.
	Skipped map[string]string
}

// skippedDirs are never descended into: dependencies, build output, generated tests and E2E
// suites are not application UI, and .git is not source.
var skippedDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, "out": true, "coverage": true,
	"e2e": true, "cypress": true, "__tests__": true, "__mocks__": true, "test": true, "tests": true,
	"__snapshots__": true, "storybook-static": true, ".next": true, ".nuxt": true, ".angular": true,
}

// PlanTargets walks the repository and selects the source files to add hooks to.
//
// Selection is by shape, not by plan item: any production component may be the one an E2E spec
// needs to address, and the alternative — walking from PAGE_ROUTE symbols to the components they
// render — needs an edge the indexer does not emit yet. The caps keep "every component" bounded.
func PlanTargets(repoRoot string, opts Options) Plan {
	opts = opts.Normalized()
	plan := Plan{Skipped: map[string]string{}}
	root := filepath.Clean(repoRoot)
	var candidates []Target
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		kind := targetKind(rel, opts)
		if kind == "" {
			return nil
		}
		if reason := skipReason(root, rel); reason != "" {
			plan.Skipped[rel] = reason
			return nil
		}
		candidates = append(candidates, Target{Rel: rel, Kind: kind})
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Rel < candidates[j].Rel })
	for i, c := range candidates {
		if i >= opts.MaxFiles {
			plan.Skipped[c.Rel] = "max_files_reached"
			continue
		}
		plan.Targets = append(plan.Targets, c)
	}
	return plan
}

// targetKind classifies a path, or returns "" for one the pass never touches.
func targetKind(rel string, opts Options) string {
	lower := strings.ToLower(rel)
	base := lower[strings.LastIndex(lower, "/")+1:]
	switch {
	case strings.HasSuffix(base, ".d.ts"):
		return ""
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, ".stories.") ||
		strings.Contains(base, ".cy.") || strings.Contains(base, ".e2e."):
		return ""
	case strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".jsx"):
		return "jsx"
	case opts.Templates && strings.HasSuffix(base, ".component.html"):
		return "html"
	}
	return ""
}

// skipReason declines a candidate the classifier accepted. Today the one rule is the snapshot
// test: a component with `__snapshots__/<file>.snap` beside it has a test that fails the moment
// an attribute appears, and that failure would land in the repository's own CI, not in ours.
func skipReason(root, rel string) string {
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(rel)))
	base := filepath.Base(rel)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, snap := range []string{base + ".snap", stem + ".test" + filepath.Ext(base) + ".snap", stem + ".spec" + filepath.Ext(base) + ".snap"} {
		if _, err := os.Stat(filepath.Join(dir, "__snapshots__", snap)); err == nil {
			return "snapshot_test_present"
		}
	}
	return ""
}
