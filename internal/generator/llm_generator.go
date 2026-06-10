package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator/llmfix"
	"github.com/asqs/asqs-core/internal/generator/contract"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/layout"
	"github.com/asqs/asqs-core/internal/workspace"
)

// LLMGenerator generates tests or docs using a ChatCompleter and the built context.
// Provider-agnostic: works with any ChatCompleter (OpenAI, Anthropic, etc.). Response content is normalized
// (e.g. extractCodeBlockContent) the same way regardless of provider so you can switch llm.provider without code changes.
// ContractRules can be set from contract.ByLang(lang) to inject per-language test generation contract (Java JUnit 5, C# xUnit).
// TestFramework is the detected JS/TS test framework (e.g. "jest", "jasmine") for generated test file naming (.spec.ts vs .test.ts).
// E2EFramework is the detected Playwright/Cypress stack for Layer e2e / E2E_SPEC items.
// DisableStructuredGenerateOutput when true skips OpenAI-style JSON-schema completions for the first attempt (runner.disable_structured_generate_output).
// TwoPhaseTestGeneration when true runs skeleton then body completion for unit tests (runner.two_phase_test_generation). See docs/DOCUMENTATION.md.
type LLMGenerator struct {
	LLM                             model.ChatCompleter
	Prompt                          string             // optional system prompt (e.g. "You are a test writer. Output only valid JUnit 5 test code.")
	ContractRules                   *contract.Contract // optional; if set, rules are appended to system prompt (prefer pure/utility first when dependencies are complicated).
	TestFramework                   string             // optional; for JS/TS: "jasmine" => .spec.ts, else => .test.ts (jest, vitest, mocha, ava)
	E2EFramework                    string             // optional; playwright, cypress, …
	DisableStructuredGenerateOutput bool
	TwoPhaseTestGeneration          bool
	// RepoPath is the absolute or cwd-relative repo root. When set for C#, unit tests may be placed under a
	// dedicated root-level tests directory (tests/, UnitTests/, …) instead of beside the source file.
	RepoPath string
	// MonoRepoWorkspace and MonoRepoTestWorkspace are normalized indexer.mono_repo_* values; when test is set, suggested paths for structured JSON / two-phase generation are remapped into the test project tree (see workspace.RemapSuggestedTestPathForMonoTestWorkspace).
	MonoRepoWorkspace     string
	MonoRepoTestWorkspace string
}

func isJSTSLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	default:
		return false
	}
}

func isCSharpLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		return true
	default:
		return false
	}
}

// jsTSE2EGenerationContract replaces the unit-test (Jest/Vitest) contract for JS/TS E2E items so Cypress/Playwright APIs are not mixed with jest.mock / unit expect.
func jsTSE2EGenerationContract(e2eFramework string) string {
	fw := strings.ToLower(strings.TrimSpace(e2eFramework))
	if fw == "" {
		fw = "playwright"
	}
	var core string
	switch fw {
	case "cypress":
		core = "Use **Cypress** only: `describe`/`it` (or `context`/`specify`), chain **`cy.*`** (`cy.visit`, `cy.get`, `cy.contains`, `should`), and Cypress assertions. **Do not** add Jest/Vitest unit-test APIs (`jest.mock`, `vi.mock`, `import` from `@jest/globals` for `test`/`expect`, or patterns meant for `npm test` unit runs) unless the existing spec file in context already combines them."
		core += " Put new specs under **`cypress/e2e/`** with filenames **`*.cy.ts`** or **`*.cy.tsx`** (Cypress `specPattern`); do **not** default to `e2e/*.spec.ts` (that is Playwright-style)."
	default:
		core = "Use **@playwright/test** only: `import { test, expect } from '@playwright/test'` (or the repo’s equivalent), `test.describe`, `page.goto`, `expect(page…)`. **Do not** use Cypress **`cy.*`** or Jest/Vitest **unit** patterns (`jest.mock`, `vi.mock`) unless the existing spec in context already does."
		core += " Put new specs under **`e2e/`** (typically **`*.spec.ts`**) unless the repo’s **playwright.config** or context shows a different `testDir`; do **not** put Playwright tests under **`cypress/e2e/*.cy.ts`**."
	}
	return "\n\nE2E generation contract (JavaScript/TypeScript):\n- " + core + "\n- Extend the existing spec style in **Similar tests** / file context only when it matches this stack; ignore Jest unit examples for API choice.\n"
}

// Generate implements Generator by sending the context to the LLM and returning the raw completion as content.
// Path is a suggested test file path derived from the symbol's file (e.g. same dir, _Test suffix).
func (g *LLMGenerator) Generate(ctx context.Context, item *retrieval.TestPlanItem, contextStr string) (content string, path string, err error) {
	if g.LLM == nil {
		return "", "", fmt.Errorf("llm generator: ChatCompleter required")
	}
	isE2E := false
	itemLang := ""
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		symKind := strings.TrimSpace(item.Gap.Symbol.Kind)
		itemLang = item.Gap.Symbol.Lang
		isE2E = strings.EqualFold(strings.TrimSpace(item.Layer), "e2e") ||
			symKind == "E2E_SPEC" || symKind == "PAGE_OBJECT" || symKind == "USER_FLOW" || symKind == "API_ROUTE" || symKind == "PAGE_ROUTE"
	}

	suggestedPath := resolvedSuggestedTestPathWithMono(item, g.TestFramework, g.E2EFramework, g.RepoPath, g.MonoRepoWorkspace, g.MonoRepoTestWorkspace)
	useStructured := !g.DisableStructuredGenerateOutput &&
		suggestedPath != "" &&
		!strings.HasPrefix(strings.TrimSpace(contextStr), ExtendExistingTestContextPrefix)

	system := g.buildGeneratorSystem(item, isE2E, itemLang, genModeSingle)
	if useStructured {
		system += structuredTestJSONSystemSuffix(suggestedPath)
	}

	runSinglePass := func(userText string) (string, string, error) {
		messages := []model.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: userText},
		}
		return g.generateFromConversation(ctx, messages, suggestedPath, useStructured)
	}

	if g.twoPhaseEligible(item, contextStr, suggestedPath, isE2E) {
		content, path, err = g.generateTwoPhase(ctx, item, contextStr, isE2E, itemLang, suggestedPath, useStructured)
	} else {
		content, path, err = runSinglePass(contextStr)
	}
	if err != nil {
		return "", "", err
	}
	if reason := lowValueGeneratedTestReason(item, isE2E, content); reason != "" {
		retryUser := contextStr + "\n\n---\nQuality retry: your previous output was rejected because it produced low-value tests (" + reason + "). " +
			"Replace reflection/existence/tautology checks with behavioral tests that invoke the production API and assert outcomes or verified mock interactions. " +
			"Do not emit `typeof(...)` + `Assert.NotNull(...)`, `CanBeReferenced`, or constructor-only smoke tests."
		content, path, err = runSinglePass(retryUser)
		if err != nil {
			return "", "", err
		}
		if reason2 := lowValueGeneratedTestReason(item, isE2E, content); reason2 != "" {
			return "", "", fmt.Errorf("llm generator: rejected low-value test output after retry (%s)", reason2)
		}
	}
	// Empty-test-shell is caught at write time by writeGeneratedFiles (via evaluator.EmptyTestFileReason);
	// we do not retry here because the item can still legitimately be a stub/skeleton (e.g. two-phase phase 1,
	// extend-existing context). The file simply will not land on disk if it has no test markers.
	return content, path, nil
}

var (
	reTypeOfNotNull = regexp.MustCompile(`(?is)typeof\s*\(.*?\).*?Assert\.NotNull\s*\(`)
	reReflectOnlyA  = regexp.MustCompile(`(?i)\bCanBeReferenced\b|\bCanCreateTestClass\b|\bCanBeConstructed\b`)
	reReflectOnlyB  = regexp.MustCompile(`(?i)Assert\.NotNull\s*\(\s*typeof\s*\(`)
)

func lowValueGeneratedTestReason(item *retrieval.TestPlanItem, isE2E bool, content string) string {
	if isE2E || strings.TrimSpace(content) == "" {
		return ""
	}
	lang := ""
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		lang = strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
	}
	if lang != "csharp" && lang != "cs" {
		return ""
	}
	if reTypeOfNotNull.MatchString(content) || reReflectOnlyB.MatchString(content) {
		return "reflection existence assertion (typeof + Assert.NotNull)"
	}
	if reReflectOnlyA.MatchString(content) && strings.Contains(content, "Assert.NotNull") {
		return "class/member existence smoke test"
	}
	return ""
}

func resolvedSuggestedTestPath(item *retrieval.TestPlanItem, testFramework, e2eFramework, repoPath string) string {
	return resolvedSuggestedTestPathWithMono(item, testFramework, e2eFramework, repoPath, "", "")
}

func resolvedSuggestedTestPathWithMono(item *retrieval.TestPlanItem, testFramework, e2eFramework, repoPath, codeMono, testMono string) string {
	path := SuggestedTestPath(item, testFramework, e2eFramework, repoPath)
	sourceRel := ""
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		sourceRel = strings.TrimSpace(item.Gap.Symbol.File)
		switch strings.TrimSpace(item.Gap.Symbol.Kind) {
		case "E2E_SPEC", "PAGE_OBJECT", "USER_FLOW":
			path = item.Gap.Symbol.File
		}
	}
	return workspace.RemapSuggestedTestPathForMonoTestWorkspace(codeMono, testMono, sourceRel, path)
}

func structuredTestJSONSystemSuffix(requiredPath string) string {
	p := filepath.ToSlash(strings.TrimSpace(requiredPath))
	return "\n\nOutput format: Your **entire** assistant message must be a **single JSON object** only: keys = repo-relative **test or spec file path(s)**, values = **full file content** as strings (use \\n for newlines in values). **Required key:** \"" + p + "\" must appear with the complete generated file for this task. No markdown fences around the whole reply, no preamble or trailing commentary."
}

func pickGeneratedContentFromPathMap(m map[string]string, suggestedPath string) string {
	suggestedPath = filepath.ToSlash(strings.TrimSpace(suggestedPath))
	if suggestedPath != "" {
		for k, v := range m {
			kn := filepath.ToSlash(strings.TrimSpace(k))
			if repoRelPathsEqual(kn, suggestedPath) && strings.TrimSpace(v) != "" {
				return v
			}
		}
		base := filepath.Base(suggestedPath)
		if base != "" && base != "." {
			for k, v := range m {
				if filepath.Base(filepath.ToSlash(strings.TrimSpace(k))) == base && strings.TrimSpace(v) != "" {
					return v
				}
			}
		}
	}
	if len(m) == 1 {
		for k, v := range m {
			if strings.TrimSpace(v) == "" {
				return ""
			}
			kn := filepath.ToSlash(strings.TrimSpace(k))
			if suggestedPath == "" || repoRelPathsEqual(kn, suggestedPath) || filepath.Base(kn) == filepath.Base(suggestedPath) {
				return v
			}
			return ""
		}
	}
	return ""
}

func (g *LLMGenerator) completeGenerateWithRetry(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 4096
	}
	const maxRetries = 3
	var result *model.CompleteResult
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err = g.LLM.Complete(ctx, messages, opts)
		if err == nil {
			return result, nil
		}
		if attempt == maxRetries-1 {
			return nil, err
		}
		if !llmfix.IsTransientNetworkError(err) {
			return nil, err
		}
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return nil, err
}

// suggestedJavaE2EPathForRouteGap maps a controller file (API_ROUTE symbol file) to a parallel integration/E2E test path.
func suggestedJavaE2EPathForRouteGap(item *retrieval.TestPlanItem) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	f := filepath.ToSlash(item.Gap.Symbol.File)
	baseFile := filepath.Base(f)
	ext := filepath.Ext(baseFile)
	name := strings.TrimSuffix(baseFile, ext)
	if ext == "" {
		ext = ".java"
	}
	if strings.Contains(f, "src/main/java") {
		testDir := filepath.ToSlash(filepath.Dir(strings.Replace(f, "src/main/java", "src/test/java", 1)))
		return filepath.Join(testDir, name+"E2EIT"+ext)
	}
	return filepath.Join("src", "test", "java", filepath.Dir(f), name+"E2EIT"+ext)
}

func isCypressE2EFramework(e2eFramework string) bool {
	return strings.EqualFold(strings.TrimSpace(e2eFramework), "cypress")
}

// suggestedJSTSE2EPathForAPIRouteGap maps a Nest/TS controller file to an E2E spec path: Playwright under e2e/api/*.e2e-spec.ts, Cypress under cypress/e2e/api/*.cy.ts (matches default specPattern).
func suggestedJSTSE2EPathForAPIRouteGap(item *retrieval.TestPlanItem, e2eFramework string) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	f := filepath.ToSlash(item.Gap.Symbol.File)
	baseFile := filepath.Base(f)
	ext := filepath.Ext(baseFile)
	if ext == "" {
		ext = ".ts"
	}
	name := strings.TrimSuffix(baseFile, ext)
	if isCypressE2EFramework(e2eFramework) {
		return filepath.Join("cypress", "e2e", "api", name+"Route.cy"+ext)
	}
	return filepath.Join("e2e", "api", name+"Route.e2e-spec"+ext)
}

// suggestedE2EPathForPageRouteGap maps a PAGE_ROUTE symbol to a spec path: Playwright under e2e/routes/*.spec.ts, Cypress under cypress/e2e/routes/*.cy.ts.
func suggestedE2EPathForPageRouteGap(item *retrieval.TestPlanItem, e2eFramework string) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	ext := filepath.Ext(item.Gap.Symbol.File)
	if ext == "" {
		ext = ".ts"
	}
	fq := item.Gap.Symbol.FQName
	const p = "PAGE_ROUTE:"
	rest := fq
	if strings.HasPrefix(fq, p) {
		rest = fq[len(p):]
	}
	pathPat := rest
	if i := strings.Index(rest, "@"); i >= 0 {
		pathPat = rest[:i]
	}
	slug := strings.Trim(pathPat, "/")
	slug = strings.ReplaceAll(slug, "/", "_")
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = "home"
	}
	slug = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, slug)
	if isCypressE2EFramework(e2eFramework) {
		return filepath.Join("cypress", "e2e", "routes", slug+".cy"+ext)
	}
	return filepath.Join("e2e", "routes", slug+".spec"+ext)
}

// behavioralUnitTestQualityHint steers the model away from trivial “source substring” tests toward real behavior checks.
func behavioralUnitTestQualityHint(item *retrieval.TestPlanItem) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
	s := "\n\n**Unit test quality (required):** Tests must **run** the code under test and assert **semantics**—return values, " +
		"promise rejections, thrown errors, and **mock/stub call arguments**. " +
		"**Forbidden:** tautological expectations (`expect(true).toBe(true)`, `expect('./path').toBe('./path')`, any `expect(expr).toBe(expr)` where both sides are the same literal or constant). Assertions must use **outputs** from invoked code, rendered UI, or verified mocks. " +
		"Do **not** treat the implementation file as a string blob: **forbidden** patterns include `fs.readFile`/`readFileSync` " +
		"on the source path plus `expect(text).toContain(...)` / regex on source to “verify” logic. That proves nothing about runtime behavior. "
	switch lang {
	case "java":
		s += "Use **JUnit 5** `Assertions.*` (or AssertJ if **Similar tests** use it) and **Mockito** (`when`, `verify`, `@Mock`, `@InjectMocks`). " +
			"**Forbidden:** `Assertions.assertTrue(true)` as the only check, empty `@Test` bodies, or `Files.readString`/`readAllLines` on the production `.java` + `assertTrue(text.contains(...))` as the main assertion. " +
			"Invoke the **public** API under test and assert return values, thrown exceptions, and collaborator interactions. " +
			"When the target logic is private and collaborators are hard-wired, avoid reflection checks; test the nearest public method that exercises the logic and mock collaborators through existing seams."
	case "javascript", "typescript", "js", "ts":
		s += "Use `jest.mock` / `vi.mock` / dependency injection so globals like `strapi.entityService` (or similar) return fixed arrays; " +
			"`await` the real exported function and `expect` boolean/object results and that mocks were invoked with expected filters. " +
			"If the module is hard to import, prefer a thin testable wrapper or mock the module path used in the repo."
	case "csharp", "cs":
		s += "Use **xUnit** `[Fact]` / `[Theory]` (or the repo’s NUnit/MSTest style from **Similar tests**) with **`Assert.*`** on return values, exceptions, and **`Verify`** on Moq mocks. " +
			"**Forbidden:** empty method bodies, `Assert.True(true)`, or comparing a variable to itself with the same literal. " +
			"Do **not** use `File.ReadAllText` on the production `.cs` path plus `Contains` on source text as the main assertion—that does not validate runtime behavior. " +
			"When the target logic is private and collaborators are hard-wired, avoid reflection checks; test the nearest public method that exercises the logic and mock collaborators through existing seams."
	default:
		s += "Use mocks/fakes for databases and remote services; assert outputs and interactions, not that source text matches an expected snippet."
	}
	return s
}

// reactTSXUnitTestHint adds React Testing Library guidance when the target looks like a UI component (tsx or RENDERS-like kinds).
func reactTSXUnitTestHint(item *retrieval.TestPlanItem) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
	if lang != "javascript" && lang != "typescript" && lang != "js" && lang != "ts" {
		return ""
	}
	kind := strings.TrimSpace(item.Gap.Symbol.Kind)
	file := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.File))
	if kind != "RENDERS" && kind != "USES_HOOK" && kind != "ACCEPTS_PROPS_TYPE" && !strings.HasSuffix(file, ".tsx") {
		return ""
	}
	return "\n\n**React / .tsx target:** Treat this as a **UI unit** test. Follow **Similar tests** in context when they use Testing Library; otherwise use `@testing-library/react` + `@testing-library/user-event`: wrap with the same providers as production if needed, `render()` the exported component, query the document (`screen.getByRole`, `findByText`, etc.), and assert **user-visible** results or **mock** invocations. Mock framework modules (`next/navigation`, router, data fetching) instead of asserting route strings against themselves."
}

// e2eGenerationActiveTestsPolicy discourages default @Disabled / test.skip on new E2E (models mimic Spring samples)
// while allowing skip only as a documented last resort.
func e2eGenerationActiveTestsPolicy() string {
	return "\n\n**Prefer active E2E tests:** Default to **runnable** tests. Do **not** add class- or method-level skips " +
		"(JUnit 5 @Disabled, JUnit 4 @Ignore, xUnit [Ignore], Playwright test.skip / describe.skip, blanket " +
		"Assumptions.assumeTrue/assumeFalse) **only** because Docker, Testcontainers, or CI might be unavailable—this " +
		"pipeline runs E2E in the project’s sandbox (often **Docker with browsers / Playwright**). Do **not** copy " +
		"defensive skip patterns from unrelated examples unless you extend an existing file that already uses the same " +
		"pattern for the same tests—and still prefer new methods **enabled**.\n\n" +
		"**Last resort:** If after a normal runnable design the test still cannot execute in any environment the repo " +
		"actually targets (e.g. missing mandatory external service with no stub path), you **may** skip **one** test " +
		"method (not the whole class unless unavoidable) with a **short, specific** reason—never a vague " +
		"\"CI/Docker unavailable\" excuse. Prefer @Disabled on a single @Test method over disabling the entire spec."
}

func e2ELLMHintForFramework(fw string) string {
	low := strings.ToLower(strings.TrimSpace(fw))
	switch low {
	case "cypress":
		return "Use Cypress APIs only (`cy.visit`, `cy.get`, etc.). Extend the existing spec when context includes file content."
	case "playwright-java":
		return "Use **Playwright for Java** (`com.microsoft.playwright.*`, Page, Locator, assertions). Extend or add methods in the existing test class/spec file. The evaluation environment may already run in a **Playwright/Java Docker image** with browsers—treat E2E as runnable, not \"CI might lack Docker\"."
	case "playwright-dotnet":
		return "Use **Microsoft.Playwright** for .NET (Playwright, IPage, Expect). Extend the existing test file when context includes it."
	case "selenium", "selenide":
		return "Use **Selenium** / **Selenide**-style browser APIs (WebDriver, @FindBy where applicable). Extend existing page objects or flow tests."
	case "playwright":
		return "Use **@playwright/test** (`test`, `expect`, `page`). Do not use Cypress `cy.*` in a Playwright spec. Extend the existing spec file when the context includes file content."
	default:
		return "Use the detected stack only for browser/E2E flows. Extend the existing spec or test file when the context includes file content."
	}
}

// extractCodeBlockContent returns only the content inside the first ``` block, if any; otherwise returns s trimmed.
// Handles LLM output that includes leading text (e.g. "Here is the test:\n\n```ts\n...\n```") or multiple blocks.
// Applied uniformly for all providers (OpenAI, Anthropic) so switching llm.provider does not require different handling.
func extractCodeBlockContent(s string) string {
	s = strings.TrimSpace(s)
	const fence = "```"
	start := strings.Index(s, fence)
	if start < 0 {
		return s
	}
	// Skip opening fence and optional language token (e.g. ```java or ```ts)
	afterOpen := start + len(fence)
	if afterOpen >= len(s) {
		return s
	}
	rest := s[afterOpen:]
	// Skip to end of first line (language tag and any spaces)
	if i := strings.Index(rest, "\n"); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = strings.TrimSpace(rest)
	}
	end := strings.Index(rest, fence)
	if end < 0 {
		// No closing fence; return from start of content to end of string
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// SuggestedTestPath returns the suggested test file path for a plan item (e.g. src/test/java/.../FooTest.java for Java).
// For JS/TS, testFramework controls the suffix: "jasmine" => .spec.ts/.spec.js, else => .test.ts/.test.js (jest, vitest, mocha, ava).
// e2eFramework selects E2E file layout for JS/TS PAGE_ROUTE / API_ROUTE: "cypress" => cypress/e2e/**/*.cy.*, otherwise Playwright-style e2e/**/*.spec.*.
// repoPath is the repo root on disk; when non-empty, C# and JS/TS may use a dedicated root-level tests folder
// (tests/, UnitTests/, …) with mirrored layout under src/ (see internal/layout).
// Used to avoid overwriting existing tests and to filter plan items that already have a test file.
func SuggestedTestPath(item *retrieval.TestPlanItem, testFramework, e2eFramework, repoPath string) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	switch strings.TrimSpace(item.Gap.Symbol.Kind) {
	case "E2E_SPEC", "PAGE_OBJECT", "USER_FLOW":
		return item.Gap.Symbol.File
	case "API_ROUTE":
		lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
		if lang == "java" {
			return suggestedJavaE2EPathForRouteGap(item)
		}
		if lang == "javascript" || lang == "typescript" {
			return suggestedJSTSE2EPathForAPIRouteGap(item, e2eFramework)
		}
		return suggestedJavaE2EPathForRouteGap(item)
	case "PAGE_ROUTE":
		return suggestedE2EPathForPageRouteGap(item, e2eFramework)
	}
	f := item.Gap.Symbol.File
	base := filepath.Base(f)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	lang := item.Gap.Symbol.Lang

	// Java: put tests under src/test/java/... (same package layout as src/main/java/...)
	if lang == "java" {
		dir := filepath.Dir(f)
		if strings.Contains(f, "src/main/java") {
			dir = filepath.Dir(strings.Replace(f, "src/main/java", "src/test/java", 1))
		} else {
			dir = filepath.Join("src", "test", "java", dir)
		}
		return filepath.Join(dir, name+"Test"+ext)
	}
	// C#: sibling *Tests.cs, or under a root-level dedicated tests/ tree when repoPath is set.
	if lang == "csharp" {
		return layout.SuggestedCSharpUnitTestPath(f, repoPath)
	}
	// JS/TS: .test. / .spec. naming; optional dedicated tests/ tree when repoPath is set (see layout).
	if lang == "javascript" || lang == "typescript" {
		return layout.SuggestedJSTSUnitTestPath(f, repoPath, testFramework)
	}
	// Default: same dir, Test suffix
	dir := filepath.Dir(f)
	return filepath.Join(dir, name+"Test"+ext)
}

// ExistingOrSuggestedTestPath returns the repo-relative test artifact path for a plan item, preferring
// an existing on-disk test file advertised via item.Context.ExistingTestPaths over the convention
// default returned by SuggestedTestPath. This is what lets the generator redirect from XTest.java /
// x.test.ts to XTests.java / x.spec.ts when that's what the repo actually uses. Returns (path, existingHit, defaultPath).
// existingHit is true iff the returned path came from ExistingTestPaths rather than the default. When
// preferDefault is true the legacy path wins (escape hatch for callers who insist on the convention).
//
// When multiple existing paths are present, one under the language-canonical test tree is preferred:
//   - Java: paths containing "src/test/java/"
//   - JS/TS / C#: paths whose first segment matches layout.DedicatedRootDirCandidates (tests/, UnitTests/, …)
//
// That keeps the redirect well-behaved for repos that also happen to carry a stray copy somewhere else.
func ExistingOrSuggestedTestPath(item *retrieval.TestPlanItem, testFramework, e2eFramework, repoPath string, preferDefault bool) (path string, existingHit bool, defaultPath string) {
	defaultPath = SuggestedTestPath(item, testFramework, e2eFramework, repoPath)
	if preferDefault || item == nil || item.Context == nil || len(item.Context.ExistingTestPaths) == 0 {
		return defaultPath, false, defaultPath
	}
	lang := ""
	if item.Gap != nil && item.Gap.Symbol != nil {
		lang = strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
	}
	chosen := pickCanonicalExistingTestPath(item.Context.ExistingTestPaths, lang)
	if chosen == "" {
		return defaultPath, false, defaultPath
	}
	// Verify the file still exists on disk when repoPath is known. This guards against stale plan
	// state where the test was deleted between planning and generation; fall back to the default in
	// that rare case so we don't silently write to a non-existent path.
	if strings.TrimSpace(repoPath) != "" {
		full := filepath.Join(repoPath, filepath.FromSlash(chosen))
		if st, err := os.Stat(full); err != nil || st.IsDir() {
			return defaultPath, false, defaultPath
		}
	}
	return chosen, true, defaultPath
}

// pickCanonicalExistingTestPath selects the best existing-test candidate given language-specific
// preferences. The input slice is assumed non-empty; the first canonical match wins, else the first
// element (callers sort ExistingTestPaths so this is deterministic).
func pickCanonicalExistingTestPath(paths []string, lang string) string {
	if len(paths) == 0 {
		return ""
	}
	canonical := ""
	switch lang {
	case "java":
		for _, p := range paths {
			if strings.Contains(filepath.ToSlash(p), "src/test/java/") {
				canonical = p
				break
			}
		}
	case "javascript", "typescript", "js", "ts", "csharp", "cs":
		for _, p := range paths {
			first := strings.SplitN(filepath.ToSlash(p), "/", 2)[0]
			for _, root := range layout.DedicatedRootDirCandidates {
				if strings.EqualFold(first, root) {
					canonical = p
					break
				}
			}
			if canonical != "" {
				break
			}
		}
	}
	if canonical != "" {
		return canonical
	}
	return paths[0]
}
