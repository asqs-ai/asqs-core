package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asqs-core run of 2026-09-04 09:05: the E2E_SPEC gap for the fixture's two-line
// e2e/smoke.e2e-spec.ts (a bare `test(`, no suite) came back as a whole Playwright file with two
// top-level suites. unwrapCompilationUnit needs exactly one primary suite, so the write was refused
// and the artifact lost. A target with no suite has nothing to splice into; the payload is appended.
func TestExtendExisting_appendsWholeJSPayloadWhenTargetHasNoSuite(t *testing.T) {
	rel := "e2e/smoke.e2e-spec.ts"
	repo := writeTemp(t, rel, "// Simulated marker\ntest('smoke: shell loads', () => {\n  void 0;\n});\n")
	payload := `import { test, expect } from '@playwright/test';
import { stubJson } from './support/api';

const CATALOG = [{ id: 1, name: 'A', unitPrice: 1 }];

test.describe('catalog', () => {
  test('lists items', async ({ page }) => {
    await stubJson(page, '**/api/catalog*', CATALOG);
    await page.goto('/catalog');
    await expect(page.getByTestId('catalog-results')).toBeVisible();
  });
});

test.describe('checkout', () => {
  test('shows total', async ({ page }) => {
    await page.goto('/checkout');
    await expect(page.getByTestId('checkout-root')).toBeVisible();
  });
});
`
	n, _, skips := Write(repo, []Item{{Path: rel, Content: payload, ExtendExisting: true, SourceSymbolFile: "src/app/app.routes.ts"}})
	if n != 1 {
		t.Fatalf("expected the extend write to land; wrote %d, skips=%v", n, skips)
	}
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"test('smoke: shell loads'", "test.describe('catalog'", "test.describe('checkout'", "const CATALOG", "from '@playwright/test'", "from './support/api'"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged file lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "from '@playwright/test'") != 1 {
		t.Errorf("import duplicated:\n%s", got)
	}
}

// A target that HAS a suite keeps the strict behaviour: a payload that cannot be reduced to one
// body is still refused rather than appended beside the existing suite.
func TestExtendExisting_stillRefusesUnwrappableJSPayloadWhenTargetHasSuite(t *testing.T) {
	rel := "e2e/routes/home.spec.ts"
	repo := writeTemp(t, rel, "import { test, expect } from '@playwright/test';\n\ntest.describe('home', () => {\n  test('loads', async ({ page }) => { await page.goto('/'); });\n});\n")
	payload := "import { test, expect } from '@playwright/test';\n\ntest.describe('a', () => {\n  test('x', async () => {});\n});\n\ntest.describe('b', () => {\n  test('y', async () => {});\n});\n"
	n, _, skips := Write(repo, []Item{{Path: rel, Content: payload, ExtendExisting: true, SourceSymbolFile: "src/app/app.routes.ts"}})
	if n != 0 || len(skips) == 0 || !strings.Contains(strings.Join(skips, "\n"), "could not be unwrapped") {
		t.Fatalf("expected the refusal to stand for a target with a suite; wrote %d, skips=%v", n, skips)
	}
}
