package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asqs-go run api-8a6ad5508ad43b22f3ff969b2d1da5cd generated a complete Playwright spec for the
// /checkout page route and threw it away: `e2e/routes/checkout.spec.ts: contains markdown code
// fence (```), LLM emitted fenced output instead of raw TypeScript/JavaScript source`. The run
// ended with no spec for that route at all. The fence check is right that the content is unusable
// AS WRITTEN; it was wrong that nothing could be done about it, because the fix path has unwrapped
// exactly this shape since it existed.
func TestWrite_unwrapsSingleFencedPayload(t *testing.T) {
	repo := t.TempDir()
	const rel = "e2e/routes/checkout.spec.ts"
	const body = "import { test, expect } from '@playwright/test';\n\ntest('checkout renders', async ({ page }) => {\n\tawait page.goto('/checkout');\n\tawait expect(page.getByTestId('checkout-total')).toBeVisible();\n});\n"

	n, paths, skips := Write(repo, []Item{{Path: rel, Content: "```typescript\n" + body + "```"}})
	if len(skips) != 0 {
		t.Fatalf("a single fenced wrapper must not be refused, got skips %v", skips)
	}
	if n != 1 || len(paths) != 1 {
		t.Fatalf("wrote %d file(s) %v, want 1", n, paths)
	}
	got, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(got), "```") {
		t.Errorf("the fence reached disk:\n%s", got)
	}
	if !strings.Contains(string(got), "test('checkout renders'") {
		t.Errorf("the spec body did not survive unwrapping:\n%s", got)
	}
}

// The Java arm of the same repair. Worth its own case because a .java payload meets two more gates
// on the way in (primary-type rename, package rewrite) and both need real source, not an envelope.
func TestWrite_unwrapsSingleFencedJava(t *testing.T) {
	repo := t.TempDir()
	const rel = "src/test/java/com/example/FencedTest.java"
	n, _, skips := Write(repo, []Item{{
		Path:    rel,
		Content: "```java\npackage com.example;\n\nimport org.junit.jupiter.api.Test;\n\nclass FencedTest {\n  @Test void a() {}\n}\n```\n",
	}})
	if len(skips) != 0 || n != 1 {
		t.Fatalf("wrote %d file(s), skips %v; want the wrapper unwrapped and written", n, skips)
	}
	got, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(got), "```") {
		t.Errorf("the fence reached disk:\n%s", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(got)), "package com.example;") {
		t.Errorf("the compilation unit did not survive unwrapping:\n%s", got)
	}
}

// The gate keeps its teeth: a reply that is prose WITH a fenced block in it is not a wrapper, and
// writing it would put markdown into a source file.
func TestWrite_stillRefusesFenceThatIsNotAWrapper(t *testing.T) {
	repo := t.TempDir()
	const rel = "e2e/routes/checkout.spec.ts"
	n, _, skips := Write(repo, []Item{{
		Path:    rel,
		Content: "Here is the spec you asked for:\n\n```typescript\nimport { test } from '@playwright/test';\ntest('a', async () => {});\n```\n\nLet me know if you need more cases.",
	}})
	if n != 0 {
		t.Fatalf("wrote %d file(s), want 0", n)
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "markdown code fence") {
		t.Fatalf("skips = %v, want one markdown-code-fence refusal", skips)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("refused content must not reach disk (stat err = %v)", err)
	}
}
