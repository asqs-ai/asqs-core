package uitesthooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, rel := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rels(ts []Target) string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Rel)
	}
	return strings.Join(out, ",")
}

// Only production UI sources: no tests, specs, stories, declarations, E2E suites, dependencies or
// build output — and templates only when asked for.
func TestPlanTargets_selectsProductionUISources(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"src/pages/HomePage.tsx", "src/pages/HomePage.test.tsx", "src/pages/Home.stories.tsx",
		"src/legacy/Banner.jsx", "src/lib/validation.ts", "src/types/global.d.ts",
		"e2e/routes/home.spec.tsx", "cypress/e2e/cart.cy.tsx", "node_modules/pkg/Comp.tsx",
		"dist/Comp.tsx", "src/__tests__/x.tsx",
		"src/app/features/catalog/catalog.component.html", "src/index.html",
	)
	p := PlanTargets(root, Options{})
	if got := rels(p.Targets); got != "src/legacy/Banner.jsx,src/pages/HomePage.tsx" {
		t.Fatalf("targets = %q", got)
	}
	p = PlanTargets(root, Options{Templates: true})
	if got := rels(p.Targets); got != "src/app/features/catalog/catalog.component.html,src/legacy/Banner.jsx,src/pages/HomePage.tsx" {
		t.Fatalf("targets with templates = %q", got)
	}
}

// A component with a snapshot test beside it is left alone: the attribute would fail that test
// in the repository's own CI.
func TestPlanTargets_skipsComponentsWithSnapshotTests(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "src/A.tsx", "src/__snapshots__/A.test.tsx.snap", "src/B.tsx")
	p := PlanTargets(root, Options{})
	if got := rels(p.Targets); got != "src/B.tsx" {
		t.Fatalf("targets = %q", got)
	}
	if p.Skipped["src/A.tsx"] != "snapshot_test_present" {
		t.Fatalf("skipped = %v", p.Skipped)
	}
}

// The cap is deterministic: the first MaxFiles in path order, the rest reported.
func TestPlanTargets_capsFilesInStableOrder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "src/C.tsx", "src/A.tsx", "src/B.tsx")
	p := PlanTargets(root, Options{MaxFiles: 2})
	if got := rels(p.Targets); got != "src/A.tsx,src/B.tsx" {
		t.Fatalf("targets = %q", got)
	}
	if p.Skipped["src/C.tsx"] != "max_files_reached" {
		t.Fatalf("skipped = %v", p.Skipped)
	}
}
