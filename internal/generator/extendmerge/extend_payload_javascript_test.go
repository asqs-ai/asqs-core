package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsExistingSuite is the artifact the first gap wrote.
const jsExistingSuite = `import { TestBed } from '@angular/core/testing';
import { HttpBackend } from '@angular/common/http';
import { LegacyInvoiceBridgeService } from './legacy-invoice-bridge.service';

jest.mock('@angular/common/http', () => ({
  ...jest.requireActual('@angular/common/http'),
  HttpClient: jest.fn(),
}));

describe('LegacyInvoiceBridgeService', () => {
  let service: LegacyInvoiceBridgeService;
  let mockHttpBackend: jest.Mocked<HttpBackend>;

  beforeEach(() => {
    mockHttpBackend = {
      handle: jest.fn(),
    } as unknown as jest.Mocked<HttpBackend>;

    TestBed.configureTestingModule({
      providers: [LegacyInvoiceBridgeService],
    });

    service = TestBed.inject(LegacyInvoiceBridgeService);
  });

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  it('should call syncLegacyInvoice when runBridge is called', () => {
    const spy = jest.spyOn(service as any, 'syncLegacyInvoice');
    service.runBridge('12345');
    expect(spy).toHaveBeenCalledWith('12345');
  });
});
`

// jsWholeFilePayload is what the second gap's model returned: the entire file again — the same
// imports, the same jest.mock, the same describe with the same declarations, the same two tests —
// plus two genuinely new ones. The prompt had asked for the new tests only.
const jsWholeFilePayload = `import { TestBed } from '@angular/core/testing';
import { HttpBackend } from '@angular/common/http';
import { LegacyInvoiceBridgeService } from './legacy-invoice-bridge.service';

jest.mock('@angular/common/http', () => ({
  ...jest.requireActual('@angular/common/http'),
  HttpClient: jest.fn(),
}));

describe('LegacyInvoiceBridgeService', () => {
  let service: LegacyInvoiceBridgeService;
  let mockHttpBackend: jest.Mocked<HttpBackend>;

  beforeEach(() => {
    mockHttpBackend = {
      handle: jest.fn(),
    } as unknown as jest.Mocked<HttpBackend>;

    TestBed.configureTestingModule({
      providers: [LegacyInvoiceBridgeService],
    });

    service = TestBed.inject(LegacyInvoiceBridgeService);
  });

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  it('should call syncLegacyInvoice when runBridge is called', () => {
    const spy = jest.spyOn(service as any, 'syncLegacyInvoice');
    service.runBridge('12345');
    expect(spy).toHaveBeenCalledWith('12345');
  });

  it('should handle null orderId correctly', () => {
    service['syncLegacyInvoice'](null as unknown as string);
    expect(true).toBe(true);
  });

  it('should encode special URL characters in orderId', () => {
    service['syncLegacyInvoice']('order?a=1&b=2');
    expect(true).toBe(true);
  });
});
`

// Stage 1: a whole file arriving under extend semantics must be recognised as a compilation unit,
// not treated as "members" and appended. Appending it is what produced an artifact with duplicate
// imports, a duplicate jest.mock and every existing test present twice.
func TestClassifyExtendPayload_typescriptWholeFileIsACompilationUnit(t *testing.T) {
	got := classifyExtendPayload("src/app/legacy/legacy-invoice-bridge.service.test.ts", jsWholeFilePayload)
	if got != payloadCompilationUnit {
		t.Fatalf("classifyExtendPayload = %v, want payloadCompilationUnit", got)
	}
}

// The other half: a genuine members-only payload must stay members-only, or every correct extend
// would be unwrapped and lose its outer structure.
func TestClassifyExtendPayload_typescriptMembersStayMembers(t *testing.T) {
	members := `  it('handles an empty id', () => {
    expect(service.runBridge('')).toBeUndefined();
  });
`
	if got := classifyExtendPayload("src/a.test.ts", members); got != payloadMembersOnly {
		t.Fatalf("classifyExtendPayload = %v, want payloadMembersOnly", got)
	}
	// A nested describe is indented, so it must not read as a top-level unit either.
	nested := `  describe('edge cases', () => {
    it('x', () => { expect(1).toBe(1); });
  });
`
	if got := classifyExtendPayload("src/a.test.ts", nested); got != payloadMembersOnly {
		t.Fatalf("nested describe: classifyExtendPayload = %v, want payloadMembersOnly", got)
	}
}

// Stage 2: the recovered payload is the describe body, so the imports and the jest.mock stay behind
// with the file header rather than being spliced into a suite.
func TestUnwrapCompilationUnit_typescriptReturnsDescribeBody(t *testing.T) {
	body, ok := unwrapCompilationUnit("src/a.test.ts", jsWholeFilePayload)
	if !ok {
		t.Fatal("a single top-level describe must be unwrappable")
	}
	for _, banned := range []string{"import ", "jest.mock(", "describe("} {
		if strings.Contains(body, banned) {
			t.Errorf("unwrapped body still carries %q:\n%s", banned, body)
		}
	}
	if !strings.Contains(body, "it('should be created'") || !strings.Contains(body, "beforeEach(") {
		t.Errorf("unwrapped body lost the suite's members:\n%s", body)
	}
}

// Two top-level describes have no primary, so the merge is refused rather than guessed.
func TestUnwrapCompilationUnit_typescriptRefusesTwoTopLevelSuites(t *testing.T) {
	two := "describe('a', () => {\n  it('x', () => {});\n});\n\ndescribe('b', () => {\n  it('y', () => {});\n});\n"
	if _, ok := unwrapCompilationUnit("src/a.test.ts", two); ok {
		t.Fatal("two top-level describes must not be unwrapped")
	}
}

// The heart of stage 2: splicing the unwrapped body verbatim would redeclare `service`,
// `mockHttpBackend` and beforeEach — a TypeScript error — and repeat both existing tests.
func TestDropDuplicateMembers_typescriptKeepsOnlyTheNewTests(t *testing.T) {
	body, ok := unwrapCompilationUnit("src/a.test.ts", jsWholeFilePayload)
	if !ok {
		t.Fatal("setup: payload must unwrap")
	}
	surviving, dropped := dropDuplicateMembers("src/a.test.ts", jsExistingSuite, body)

	for _, want := range []string{"decl:service", "decl:mockHttpBackend", "hook:beforeEach",
		"test:should be created", "test:should call syncLegacyInvoice when runBridge is called"} {
		found := false
		for _, d := range dropped {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("member %q was already declared and must be dropped; dropped = %v", want, dropped)
		}
	}
	if strings.Contains(surviving, "let service") || strings.Contains(surviving, "beforeEach(") {
		t.Errorf("surviving payload still redeclares the suite's own members:\n%s", surviving)
	}
	if strings.Count(surviving, "it(") != 2 {
		t.Errorf("want exactly the 2 new tests, got %d:\n%s", strings.Count(surviving, "it("), surviving)
	}
	for _, want := range []string{"should handle null orderId correctly", "should encode special URL characters in orderId"} {
		if !strings.Contains(surviving, want) {
			t.Errorf("the new test %q must survive:\n%s", want, surviving)
		}
	}
}

// End to end through the real write path: the merged file must have one describe, one import block
// and each test exactly once.
func TestWriteGeneratedFiles_typescriptExtendMergesInsteadOfAppending(t *testing.T) {
	repo := t.TempDir()
	const rel = "src/app/legacy/legacy-invoice-bridge.service.test.ts"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(jsExistingSuite), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, _, skips := Write(repo, []Item{{
		Path:           rel,
		Content:        jsWholeFilePayload,
		ExtendExisting: true,
	}})
	if wrote != 1 {
		t.Fatalf("write count = %d, skips = %v", wrote, skips)
	}

	merged, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	got := string(merged)
	if n := strings.Count(got, "describe('LegacyInvoiceBridgeService'"); n != 1 {
		t.Errorf("describe appears %d time(s), want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "import { TestBed }"); n != 1 {
		t.Errorf("import appears %d time(s), want 1", n)
	}
	if n := strings.Count(got, "jest.mock("); n != 1 {
		t.Errorf("jest.mock appears %d time(s), want 1", n)
	}
	if n := strings.Count(got, "it('should be created'"); n != 1 {
		t.Errorf("existing test appears %d time(s), want 1", n)
	}
	if n := strings.Count(got, "let service"); n != 1 {
		t.Errorf("`let service` appears %d time(s), want 1 — a redeclaration will not compile", n)
	}
	for _, want := range []string{"should handle null orderId correctly", "should encode special URL characters in orderId"} {
		if !strings.Contains(got, want) {
			t.Errorf("the new test %q is missing from the merge:\n%s", want, got)
		}
	}
	// The new tests must land INSIDE the suite, which is what mergedPayloadInsideTypeBody enforces.
	if _, closeIdx, ok := jsPrimaryDescribeBodyRange(got); !ok {
		t.Error("merged file no longer has a locatable describe block")
	} else if tail := strings.Trim(strings.TrimSpace(got[closeIdx+1:]), ");"); tail != "" {
		// The describe's `}` is followed by the call's own `);` — anything else means the payload
		// landed outside the suite.
		t.Errorf("content landed after the describe closed:\n%q", got[closeIdx+1:])
	}
}

// The scanner must survive the constructs that defeat naive brace counting.
func TestJSPrimaryDescribeBodyRange_survivesTrickyLiterals(t *testing.T) {
	src := "describe('s', () => {\n" +
		"  const re = /\\{[^}]*\\}/;\n" +
		"  const tpl = `a ${ { k: '}' } } b`;\n" +
		"  // a } in a comment\n" +
		"  const str = 'closing } brace';\n" +
		"  it('x', () => { expect(re).toBeTruthy(); });\n" +
		"});\n"
	_, closeIdx, ok := jsPrimaryDescribeBodyRange(src)
	if !ok {
		t.Fatal("regex, template and comment braces must not defeat the scan")
	}
	if tail := strings.Trim(strings.TrimSpace(src[closeIdx+1:]), ");"); tail != "" {
		t.Errorf("close brace resolved to the wrong place; tail = %q", src[closeIdx+1:])
	}
}

// JSX cannot be scanned reliably, so .tsx keeps the conservative behaviour: no range, which means
// classifyExtendPayload refuses rather than merging.
func TestJSPrimaryDescribeBodyRange_bailsOnJSX(t *testing.T) {
	src := "describe('s', () => {\n  it('x', () => { render(<div />); });\n});\n"
	if _, _, ok := jsPrimaryDescribeBodyRange(src); ok {
		t.Fatal("JSX must yield no range")
	}
}

// Unsupported shapes must leave the historical behaviour untouched.
func TestJSExtendHelpers_conservativeFallbacks(t *testing.T) {
	if isJSExtendPath("src/Foo.java") || isJSExtendPath("src/a.test.tsx") {
		t.Error("only the non-JSX JS/TS suffixes are in scope")
	}
	// No describe at all: not a compilation unit, and nothing to unwrap.
	loose := "  it('x', () => { expect(1).toBe(1); });\n"
	if jsPayloadIsCompilationUnit(loose) {
		t.Error("a bare it() list is not a compilation unit")
	}
	if _, ok := unwrapCompilationUnit("src/a.test.ts", loose); ok {
		t.Error("nothing to unwrap in a members-only payload")
	}
	// A body the splitter cannot terminate leaves the payload untouched rather than half-cut.
	if _, dropped := dropDuplicateMembers("src/a.test.ts", jsExistingSuite, "it('x', () => {}"); dropped != nil {
		t.Errorf("an unparseable payload must be left alone, dropped = %v", dropped)
	}
}

// jsPlaywrightSuite is the dialect the E2E gaps generate: `test.describe` rather than `describe`.
const jsPlaywrightSuite = `import { test, expect } from '@playwright/test';

test.describe('smoke', () => {
  test('bootstrap smoke', () => {
    expect(true).toBeTruthy();
  });
});
`

// The regression for run 2026-09-01T08:36Z, where two E2E gaps lost their artifacts to
// "extend payload is a full compilation unit and could not be unwrapped": the payload was a
// Playwright spec, and only `describe(` was recognised as a top-level suite.
func TestUnwrapCompilationUnit_playwrightSuite(t *testing.T) {
	body, ok := unwrapCompilationUnit("e2e/route.spec.ts", jsPlaywrightSuite)
	if !ok {
		t.Fatal("test.describe is a top-level suite and must unwrap")
	}
	if strings.Contains(body, "import ") || strings.Contains(body, "test.describe(") {
		t.Errorf("unwrapped body kept the file header:\n%s", body)
	}
	if !strings.Contains(body, "test('bootstrap smoke'") {
		t.Errorf("unwrapped body lost its test:\n%s", body)
	}
}

// Merging across dialects is refused rather than attempted: a Playwright spec spliced into a Jest
// suite type-checks and then fails at run time on a fixture Jest never supplies. This pairing is
// reachable — an E2E gap can resolve its artifact to a source file's unit test path.
func TestJSSuiteKindsCompatible(t *testing.T) {
	if JSSuiteKindsCompatible(jsExistingSuite, jsPlaywrightSuite) {
		t.Error("a Playwright payload must not merge into a Jest suite")
	}
	if JSSuiteKindsCompatible(jsPlaywrightSuite, jsExistingSuite) {
		t.Error("a Jest payload must not merge into a Playwright suite")
	}
	if !JSSuiteKindsCompatible(jsExistingSuite, jsWholeFilePayload) {
		t.Error("same-dialect merges must stay allowed")
	}
	if !JSSuiteKindsCompatible(jsPlaywrightSuite, jsPlaywrightSuite) {
		t.Error("Playwright to Playwright must be allowed")
	}
	// An unreadable side keeps the historical behaviour rather than refusing.
	if !JSSuiteKindsCompatible("export const x = 1;\n", jsPlaywrightSuite) {
		t.Error("no locatable suite means no opinion")
	}
}

// End to end: the write path refuses the cross-dialect merge with a reason instead of producing a
// file that mixes both frameworks.
func TestWriteGeneratedFiles_refusesCrossDialectExtend(t *testing.T) {
	repo := t.TempDir()
	const rel = "src/app/app.routes.test.ts"
	full := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(jsExistingSuite), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, _, skips := Write(repo, []Item{{
		Path:           rel,
		Content:        jsPlaywrightSuite,
		ExtendExisting: true,
	}})
	if wrote != 0 {
		t.Fatalf("cross-dialect extend must not write, wrote %d", wrote)
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "different test dialect") {
		t.Fatalf("skips = %v, want the dialect reason", skips)
	}
	after, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != jsExistingSuite {
		t.Error("the file on disk must be untouched")
	}
}
