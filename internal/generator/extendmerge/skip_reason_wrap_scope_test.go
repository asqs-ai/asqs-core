package extendmerge

import (
	"path/filepath"
	"strings"
	"testing"
)

// asqs-go run api-fd5599a24f84dbe71e0c831b3266e4f4 refused e2e/routes/catalog.spec.ts with
// "contains markdown code fence (```) …; wrap declined: not a .java file". The fence diagnosis is
// right; the appended decline is noise, because the members-wrap recovery is Java and C# only and
// can never apply to a .ts file. A reason that reports a recovery nobody attempted sends the reader
// after the wrong thing.
func TestWrite_skipReasonOmitsTheWrapForLanguagesItCannotServe(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("e2e", "routes", "catalog.spec.ts"))
	fenced := "```typescript\nimport { test, expect } from '@playwright/test';\ntest('x', async () => { expect(1).toBe(1); });\n```\n"

	n, _, skips := Write(repo, []Item{{Path: rel, Content: fenced}})

	if n != 0 || len(skips) != 1 {
		t.Fatalf("wrote %d file(s), skips %v; want the fenced payload refused", n, skips)
	}
	if !strings.Contains(skips[0], "markdown code fence") {
		t.Errorf("the fence must still be named: %q", skips[0])
	}
	if strings.Contains(skips[0], "wrap declined") {
		t.Errorf("no wrap is attempted for .ts, so none may be reported: %q", skips[0])
	}
}

// Java and C# keep the decline: there the wrap IS attempted and why it failed is actionable.
func TestWrite_skipReasonKeepsTheWrapWhereItIsAttempted(t *testing.T) {
	for _, tc := range []struct{ rel, content string }{
		{filepath.ToSlash(filepath.Join("src", "test", "java", "p", "FooTest.java")), "```java\n@Test void t() {}\n```\n"},
		{filepath.ToSlash(filepath.Join("tests", "FooTests.cs")), "```csharp\n[Fact] public void T() {}\n```\n"},
	} {
		t.Run(filepath.Ext(tc.rel), func(t *testing.T) {
			repo := t.TempDir()
			n, _, skips := Write(repo, []Item{{Path: tc.rel, Content: tc.content}})
			if n != 0 || len(skips) != 1 {
				t.Fatalf("wrote %d, skips %v", n, skips)
			}
			if !strings.Contains(skips[0], "wrap declined") {
				t.Errorf("the attempted wrap's decline must be reported: %q", skips[0])
			}
		})
	}
}
