package evaluator

import (
	"strings"
	"testing"
)

// jsCleanFixture is the shape the gate must never object to: quotes containing slashes, line
// comments, generics, decimals — everything the Angular test that triggered this check had, minus
// the corruption.
const jsCleanFixture = `import { ComponentFixture, TestBed } from '@angular/core/testing';
import { CheckoutComponent } from './checkout.component';

// Mock the PricingService
jest.mock('./pricing.service', () => {
  return { PricingService: jest.fn() };
});

describe('CheckoutComponent', () => {
  let fixture: ComponentFixture<CheckoutComponent>;

  it('computes a line total', () => {
    // Arrange
    const quantity = 5;
    const unitPrice = 10.99;
    const expectedTotal = 54.95; // 5 * 10.99 = 54.95
    expect(expectedTotal).toBe(54.95);
  });
});
`

// TestJSSyntacticShellReason_straySlashFromBrokenEscape is the regression for run
// api-f1d4227cb6db875a2e51c3100b3e1be8: the model's structured output carried an illegal `\ ` escape
// where `\n` belonged, so the artifact reached disk with a backslash in code position and shipped in
// the PR untouched by any gate.
func TestJSSyntacticShellReason_straySlashFromBrokenEscape(t *testing.T) {
	broken := strings.Replace(
		jsCleanFixture,
		"    const quantity = 5;\n    const unitPrice = 10.99;",
		"    const quantity = 5;\\      const unitPrice = 10.99;",
		1,
	)
	if broken == jsCleanFixture {
		t.Fatal("test setup: the corruption was not applied")
	}
	reason := SyntacticShellReason("src/app/features/checkout/checkout.component.test.ts", broken)
	if reason == "" {
		t.Fatal("a backslash in code position must be refused")
	}
	for _, want := range []string{"stray backslash", "line "} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q should mention %q", reason, want)
		}
	}
}

// The clean twin: every construct in the fixture is legal and must pass.
func TestJSSyntacticShellReason_acceptsCleanSource(t *testing.T) {
	for _, path := range []string{
		"src/a.test.ts", "src/a.spec.ts", "src/a.test.js", "src/a.mjs", "src/a.cjs", "src/a.mts",
	} {
		if reason := SyntacticShellReason(path, jsCleanFixture); reason != "" {
			t.Errorf("%s: clean source rejected: %s", path, reason)
		}
	}
}

func TestJSSyntacticShellReason_fence(t *testing.T) {
	fenced := "```ts\n" + jsCleanFixture + "```\n"
	if reason := SyntacticShellReason("src/a.test.ts", fenced); !strings.Contains(reason, "markdown code fence") {
		t.Errorf("reason = %q, want the fence check to fire", reason)
	}
}

// Backslashes that belong to the language must not be reported. Each of these would be a false
// positive, and a false positive on the fix path silently discards a correct repair.
func TestJSSyntacticShellReason_legalBackslashesArePassed(t *testing.T) {
	cases := map[string]string{
		"single-quoted escape": `const s = 'a\nb';` + "\n",
		"double-quoted escape": `const s = "a\tb";` + "\n",
		"escaped quote":        `const s = 'it\'s';` + "\n",
		"windows path":         `const p = "C:\\dir\\file";` + "\n",
		"template literal":     "const s = `line\\nbreak`;\n",
		"template with interp": "const s = `a ${ b } c\\n`;\n",
		"nested interp braces": "const s = `${ { k: 'v\\n' } }`;\n",
		"line comment":         "// a \\ backslash in prose\nconst x = 1;\n",
		"block comment":        "/* a \\ backslash in prose */\nconst x = 1;\n",
	}
	for name, src := range cases {
		if reason := SyntacticShellReason("src/a.test.ts", src); reason != "" {
			t.Errorf("%s: false positive: %s\nsource: %s", name, reason, src)
		}
	}
}

// Constructs the scanner cannot tokenize exactly must yield NO opinion rather than a guess. A `/`
// in code position is a regex, a division or a JSX tag close, and reading it wrong desynchronises
// everything after it.
func TestJSSyntacticShellReason_bailsOnUndecidableConstructs(t *testing.T) {
	cases := map[string]string{
		"regex literal":         "const re = /ab+c/;\nconst bad = \\;\n",
		"division":              "const half = total / 2;\nconst bad = \\;\n",
		"jsx close":             "const el = <div />;\nconst bad = \\;\n",
		"unterminated string":   "const s = 'oops\nconst bad = \\;\n",
		"unterminated template": "const s = `oops\nconst bad = \\;\n",
		"unterminated comment":  "/* oops\nconst bad = \\;\n",
	}
	for name, src := range cases {
		if reason := SyntacticShellReason("src/a.test.ts", src); reason != "" {
			t.Errorf("%s: scanner must stay silent when it cannot stay in sync, got: %s", name, reason)
		}
	}
}

// The stray backslash must still be found after the constructs the scanner CAN track, so the check
// is not accidentally limited to the first few lines.
func TestJSStrayBackslash_reportsPositionAfterTrackedConstructs(t *testing.T) {
	src := "const a = 'x';\n" +
		"// comment\n" +
		"const t = `tpl ${ 'in' } end`;\n" +
		"const bad = \\;\n"
	line, col, found := jsStrayBackslash(src)
	if !found {
		t.Fatal("the stray backslash after a template literal must be found")
	}
	if line != 4 {
		t.Errorf("line = %d, want 4", line)
	}
	if col != 13 {
		t.Errorf("col = %d, want 13", col)
	}
}

// Other languages must be untouched by this branch.
func TestSyntacticShellReason_otherExtensionsUnaffected(t *testing.T) {
	if reason := SyntacticShellReason("src/notes.md", `a \ backslash`); reason != "" {
		t.Errorf("unknown extensions must return no opinion, got %q", reason)
	}
	if reason := SyntacticShellReason("src/a.test.ts", "   \n\t\n"); reason != "" {
		t.Errorf("whitespace-only content is the caller's fix_skip_empty case, got %q", reason)
	}
}
