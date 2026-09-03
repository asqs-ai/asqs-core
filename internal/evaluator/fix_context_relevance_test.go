package evaluator

import (
	"strings"
	"testing"
)

// THE REGRESSION THIS TIER EXISTS FOR — reproduced from run
// api-c81d90a22d1460d87b64e483837fdc24 at the shape that matters.
//
// The fixer was repairing `No provider for InjectionToken API_BASE_URL` in catalog.service.test.ts.
// catalog.service.ts is the largest read-only candidate AND the only file naming both
// `inject(API_BASE_URL)` and the token's import path, so largest-first threw away the one file that
// could answer the question and kept smaller ones that could not.
func TestClampFixContextRunes_keepsTheSourceTheFailureBlames(t *testing.T) {
	const stack = `NullInjectorError: No provider for InjectionToken API_BASE_URL!
    at new CatalogService (src/app/features/catalog/catalog.service.ts:37:8)`

	files := map[string]string{
		// Writable artifact: exempt, and it imports the service under test.
		"src/app/features/catalog/catalog.service.test.ts": "import { CatalogService } from './catalog.service';" + strings.Repeat(" ", 60),
		// The answer, and the largest dependency.
		"src/app/features/catalog/catalog.service.ts": strings.Repeat("s", 400),
		// Smaller and irrelevant: these are what largest-first used to keep.
		"src/app/features/checkout/pricing.service.ts":      strings.Repeat("p", 200),
		"src/app/shared/pipes/currency-display.pipe.ts":     strings.Repeat("c", 200),
		"src/app/features/dashboard/dashboard.component.ts": strings.Repeat("d", 200),
	}
	// 600 leaves room for the artifact (~110) plus catalog.service.ts (400) once the three
	// irrelevant dependencies are shed.
	dropped := clampFixContextRunes(files, []string{"src/app/features/catalog/catalog.service.test.ts"}, 600, stack)

	if _, ok := files["src/app/features/catalog/catalog.service.ts"]; !ok {
		t.Fatalf("the source the stack trace blames was dropped; dropped=%v", dropped)
	}
	for _, d := range dropped {
		if d == "src/app/features/catalog/catalog.service.ts" {
			t.Errorf("catalog.service.ts must be shed last, not first: %v", dropped)
		}
	}
	if len(dropped) == 0 {
		t.Error("the budget was exceeded; something had to go")
	}
}

// Relevance outranks size, and size still decides within a tier.
func TestClampFixContextRunes_shedsIrrelevantBeforeRelevant(t *testing.T) {
	files := map[string]string{
		"src/FooTest.java":    strings.Repeat("a", 50), // artifact
		"src/Blamed.java":     strings.Repeat("b", 400),
		"src/BigNoise.java":   strings.Repeat("c", 300),
		"src/SmallNoise.java": strings.Repeat("d", 100),
	}
	// 560 is met by shedding BigNoise alone (850-300), so only one drop is needed.
	dropped := clampFixContextRunes(files, []string{"src/FooTest.java"}, 560, "error at src/Blamed.java:12")

	if _, ok := files["src/Blamed.java"]; !ok {
		t.Errorf("the blamed file must survive despite being the largest; dropped=%v", dropped)
	}
	if len(dropped) != 1 || dropped[0] != "src/BigNoise.java" {
		t.Fatalf("want the largest IRRELEVANT file shed first, got %v", dropped)
	}
}

// A dependency an artifact imports is relevant even when the failure never names it — the module
// name is what an import of a repo file looks like once the extension is stripped.
func TestClampFixContextRunes_keepsWhatAnArtifactImports(t *testing.T) {
	files := map[string]string{
		"src/app/thing.test.ts": "import { Thing } from './thing';",
		"src/app/thing.ts":      strings.Repeat("t", 400),
		"src/app/unrelated.ts":  strings.Repeat("u", 300),
	}
	// 450 is met by shedding unrelated.ts alone.
	dropped := clampFixContextRunes(files, []string{"src/app/thing.test.ts"}, 450, "some failure naming nothing")

	if _, ok := files["src/app/thing.ts"]; !ok {
		t.Errorf("an imported dependency must outrank an unimported one; dropped=%v", dropped)
	}
	if len(dropped) != 1 || dropped[0] != "src/app/unrelated.ts" {
		t.Fatalf("want src/app/unrelated.ts shed, got %v", dropped)
	}
}

// When the budget cannot fit even the relevant files, they are shed too — an over-budget prompt is
// the one thing worse than a thin one. Order still holds: irrelevant goes first.
func TestClampFixContextRunes_shedsRelevantOnlyWhenItMust(t *testing.T) {
	files := map[string]string{
		"src/a.test.ts": strings.Repeat("a", 10),
		"src/blamed.ts": strings.Repeat("b", 300),
		"src/noise.ts":  strings.Repeat("n", 300),
	}
	dropped := clampFixContextRunes(files, []string{"src/a.test.ts"}, 50, "at src/blamed.ts:1")

	if len(dropped) != 2 {
		t.Fatalf("both dependencies had to go, got %v", dropped)
	}
	if _, ok := files["src/a.test.ts"]; !ok {
		t.Error("the writable artifact must survive an impossible budget")
	}
}

func TestRelevantDependencyPaths_signals(t *testing.T) {
	files := map[string]string{
		"src/app/x.test.ts":  "import { X } from './x';",
		"src/app/x.ts":       "class X {}",
		"src/app/blamed.ts":  "whatever",
		"src/app/nothing.ts": "whatever",
	}
	protected := map[string]bool{"src/app/x.test.ts": true}
	got := relevantDependencyPaths(files, protected, "boom at src/app/blamed.ts:4:2")

	if !got["src/app/x.ts"] {
		t.Error("a module the artifact imports must be relevant")
	}
	if !got["src/app/blamed.ts"] {
		t.Error("a file the failure names must be relevant")
	}
	if got["src/app/nothing.ts"] {
		t.Error("an unreferenced, unnamed file must not be relevant")
	}
	if got["src/app/x.test.ts"] {
		t.Error("writable artifacts are exempt and must not appear in the relevance set")
	}
}
