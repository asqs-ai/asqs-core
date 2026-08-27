package generator

import (
	"context"
	"fmt"
	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"golang.org/x/sync/singleflight"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator/llmfix"
	"github.com/asqs/asqs-core/internal/generator/contract"
	"github.com/asqs/asqs-core/internal/genmanifest"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
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
	// Tools optionally gives the model read-only access to the index during generation. Nil is the
	// one-shot path: retrieval assembles a context and the model gets a single turn.
	Tools tools.ToolInvoker
	// ToolLoop bounds that access. Its zero Mode means one-shot regardless of Tools.
	ToolLoop tools.LoopOptions
	// APISurface, when set, resolves real member signatures from the compile classpath for the
	// third-party types a generated test is about to call into. Optional: nil means generation
	// behaves exactly as it did before this existed.
	APISurface apisurface.Provider

	// apiSurfaceMu guards apiSurfaceBlocks, which caches one rendered block per distinct TARGET
	// SET for the life of the generator — not one block for the whole run. The target set varies
	// with isE2E, so a single cached block let a unit gap's targets serve every E2E gap: upstream
	// measured a run whose only api_surface event requested five Spring annotations and no
	// Playwright types at all, despite e2e_framework=playwright-java.
	apiSurfaceMu     sync.Mutex
	apiSurfaceBlocks map[string]apiSurfaceEntry
	apiSurfaceGroup  singleflight.Group
	// RepoPath is the absolute or cwd-relative repo root. When set for C#, unit tests may be placed under a
	// dedicated root-level tests directory (tests/, UnitTests/, …) instead of beside the source file.
	RepoPath string
	// MonoRepoWorkspace and MonoRepoTestWorkspace are normalized indexer.mono_repo_* values; when test is set, suggested paths for structured JSON / two-phase generation are remapped into the test project tree (see workspace.RemapSuggestedTestPathForMonoTestWorkspace).
	MonoRepoWorkspace     string
	MonoRepoTestWorkspace string
	// Audit is optional; when set, output-truncation events (llm.output_truncated,
	// llm.output_truncated_retry) and provider completion warnings are recorded so the frequency of
	// provider-side truncation becomes visible instead of presenting as "the model produced garbage".
	Audit Auditor
}

// Auditor is the audit sink this package needs (the same two-method shape the pipeline's auditor
// satisfies everywhere else).
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
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
	// Appended before the structured-output suffix so the output-format instruction stays last,
	// where a small model weights it most heavily. Only the framework block belongs here: it is
	// stable per (language, layer, framework), which is what makes caching it sound.
	system += g.pregenerateAPISurface(ctx, itemLang, isE2E)
	// The bootstrap contract, when one exists, is the most reliable statement available about what
	// is on the test classpath, so it outranks anything the model infers from build files. Absent
	// (bootstrap disabled — the default — or the repository already had its tooling) it renders
	// nothing and the prompt is byte-identical to what it was before this existed.
	system += g.testStackSystemBlock()
	if useStructured {
		system += structuredTestJSONSystemSuffix(suggestedPath)
	}
	// The per-symbol block goes in the USER message, where varying it per gap costs nothing.
	contextStr += g.signatureAPISurface(ctx, item, itemLang)

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
	// A member the classpath proves exists under a different CASE is a repair, not a retry: the
	// model had the right member and the wrong capitalisation, and a whole extra round to say so
	// costs more than the substitution.
	content = g.repairMemberCase(ctx, content, item, itemLang, isE2E)
	if reason := lowValueGeneratedTestReason(item, isE2E, content); reason != "" {
		retryUser := contextStr + "\n\n---\nQuality retry: your previous output was rejected because it produced low-value tests (" + reason + "). " +
			"Replace reflection/existence/tautology checks with behavioral tests that invoke the production API and assert outcomes or verified mock interactions. " +
			"Do not emit `typeof(...)` + `Assert.NotNull(...)`, `CanBeReferenced`, or constructor-only smoke tests."
		content, path, err = runSinglePass(retryUser)
		if err != nil {
			return "", "", err
		}
		content = g.repairMemberCase(ctx, content, item, itemLang, isE2E)
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

// DefaultGenerateMaxTokens is the output cap for test generation when the caller sets none.
//
// Raised from 4096: a full JUnit or xUnit test class with several test methods routinely exceeds
// 4096 output tokens, and a structured response is a JSON object mapping path -> whole file content,
// which makes the payload larger still. The fixer already used 8192.
const DefaultGenerateMaxTokens = 8192

// maxGenerateOutputTokens bounds the truncation retry so a pathological gap cannot escalate without
// limit. One doubling from the default lands here.
const maxGenerateOutputTokens = 16384

// truncationEscalations bounds how often one generation may re-ask at a larger output cap. It is a
// separate budget from the transient retries, because a truncated completion is not a failed
// request: retrying the same request reproduces it exactly, so the escalation asks a DIFFERENT
// question (a larger cap). Two escalations means a caller-set 4096 can still reach
// maxGenerateOutputTokens.
const truncationEscalations = 2

// completeOnce performs one generation attempt, with tool access when it is configured.
//
// The tool loop sits INSIDE the truncation retry: a completion cut short at max_tokens is retried as
// a whole, tool calls included, because the retry re-asks the same question at a larger cap and the
// lookups that informed the first attempt inform the second equally.
func (g *LLMGenerator) completeOnce(ctx context.Context, messages []model.Message, opts model.CompleteOptions, budget *tools.RunBudget) (*model.CompleteResult, error) {
	if g.Tools == nil || g.ToolLoop.Mode == "" || g.ToolLoop.Mode == tools.ModeOneShot {
		return g.LLM.Complete(ctx, messages, opts)
	}
	loop := g.ToolLoop
	// Attempts are audited per call. The hook is attached here rather than stored on the generator
	// so it carries this call's ctx, which is what ties an attempt to its run in the audit log.
	// (Upstream additionally composes in a context-carried observer installed by the session
	// runner, so each lookup is also recorded as a gap-attributed session attempt; the session
	// engine is outside core's seam, so there is nothing to compose with here.)
	if loop.OnAttempt == nil {
		loop.OnAttempt = g.auditToolAttempts(ctx)
	}
	if loop.OnCapHit == nil {
		loop.OnCapHit = g.auditToolCapHit(ctx)
	}
	// The per-gap budget is supplied by the caller, not created here: this function runs once per
	// retry attempt, so a budget created here would reset on every retry.
	loop.Budget = budget
	return tools.CompleteWithTools(ctx, g.LLM, g.Tools, messages, opts, loop)
}

func (g *LLMGenerator) completeGenerateWithRetry(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if opts.MaxTokens == 0 {
		opts.MaxTokens = DefaultGenerateMaxTokens
	}
	// ONE application-level retry, not three (M-18). This loop sits on top of the provider
	// clients' own transport retry — 5 attempts with exponential backoff and jitter (OpenAI,
	// Ollama, and Anthropic via retryhttp, all DefaultMaxAttempts = 5). Three application
	// attempts over five transport attempts is up to 15 requests for one completion, and the
	// callers stack further: the structured→unstructured fallback and the quality retry each
	// call this path again. Against a flapping provider that is a self-inflicted load
	// multiplier, and every request is billed.
	//
	// Resilience is not reduced below the retry bundle's baseline, because that baseline IS the
	// transport retry and it is untouched: after five attempts with backoff have failed, a sixth
	// issued immediately afterwards is not a meaningfully different bet. The truncation
	// escalation keeps its own separate budget — it re-asks a DIFFERENT question (a larger
	// output cap), so it must not draw from this one.
	const maxRetries = 2
	// One budget for this gap, shared by every attempt below. Retries re-ask the same question, so
	// their lookups draw from the same allowance rather than starting fresh — a budget created per
	// attempt would reset on every retry and the per-run cap would bound nothing. Measured upstream
	// before this was wired: one gap made 8 loop invocations and 60 tool calls against a cap of 12.
	budget := &tools.RunBudget{}
	var result *model.CompleteResult
	var err error
	escalations := 0
	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err = g.completeOnce(ctx, messages, opts, budget)
		if err == nil {
			// Provider-side conditions that did not fail the call but change how to read it —
			// notably an Ollama prompt that filled num_ctx and was silently truncated at the
			// front, taking the system prompt with it. Auditing it here is what turns "the model
			// ignored the output contract" into "the model never received the output contract".
			g.auditCompletionWarnings(ctx, result)
			return result, nil
		}
		// A truncated completion is not a transport failure: retrying the same request reproduces
		// it exactly. Re-ask at a larger cap, then surface it. Never fall through to the partial
		// content — the defensive path/content parsers would happily extract a fragment from a JSON
		// object cut mid-string and write it to disk. Escalations do not consume transient attempts.
		if trunc, ok := model.IsTruncatedCompletion(err); ok {
			if opts.MaxTokens < maxGenerateOutputTokens && escalations < truncationEscalations {
				was := opts.MaxTokens
				opts.MaxTokens = min(was*2, maxGenerateOutputTokens)
				escalations++
				attempt--
				g.auditTruncation(ctx, "llm.output_truncated_retry", trunc, was, opts.MaxTokens)
				continue
			}
			g.auditTruncation(ctx, "llm.output_truncated", trunc, opts.MaxTokens, 0)
			return nil, err
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

func (g *LLMGenerator) auditTruncation(ctx context.Context, step string, trunc *model.TruncatedCompletionError, was, now int) {
	if g.Audit == nil || trunc == nil {
		return
	}
	payload := map[string]interface{}{
		"provider":       trunc.Provider,
		"stop_reason":    trunc.Reason,
		"max_tokens":     was,
		"produced":       trunc.GotTokens,
		"partial_length": len(trunc.Content),
	}
	if now > 0 {
		payload["retry_max_tokens"] = now
	}
	g.Audit.Log(ctx, step, payload)
}

// auditCompletionWarnings records non-fatal provider warnings attached to a completion.
func (g *LLMGenerator) auditCompletionWarnings(ctx context.Context, res *model.CompleteResult) {
	if g.Audit == nil || res == nil || len(res.Warnings) == 0 {
		return
	}
	for _, w := range res.Warnings {
		g.Audit.Log(ctx, "llm.completion_warning", map[string]interface{}{
			"message": w,
		})
	}
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
		if lang == "csharp" || lang == "cs" {
			return layout.SuggestedCSharpE2ETestPath(item.Gap.Symbol.File, repoPath)
		}
		return suggestedJavaE2EPathForRouteGap(item)
	case "PAGE_ROUTE":
		if lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang)); lang == "csharp" || lang == "cs" {
			return layout.SuggestedCSharpE2ETestPath(item.Gap.Symbol.File, repoPath)
		}
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
		return applyUnitSuffixConvention(filepath.ToSlash(filepath.Join(dir, name+"Test"+ext)), lang, conventionForItem(item))
	}
	// C#: sibling *Tests.cs, or under a root-level dedicated tests/ tree when repoPath is set.
	if lang == "csharp" {
		return applyUnitSuffixConvention(layout.SuggestedCSharpUnitTestPath(f, repoPath), lang, conventionForItem(item))
	}
	// JS/TS: .test. / .spec. naming; optional dedicated tests/ tree when repoPath is set (see layout).
	if lang == "javascript" || lang == "typescript" {
		return applyUnitSuffixConvention(layout.SuggestedJSTSUnitTestPath(f, repoPath, testFramework), lang, conventionForItem(item))
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
	// Layer gate BEFORE canonical selection. ExistingTestPaths is keyed on the SOURCE file, which
	// the unit and e2e plan layers share, so without this an e2e gap redirects into the unit suite
	// (and vice versa) — silently discarding the path its own suggester produced. Falling through
	// to defaultPath here is the desired outcome: that IS the layer's suggested path.
	candidates, _ := existingTestPathsForItem(item)
	if len(candidates) == 0 {
		return defaultPath, false, defaultPath
	}
	chosen := pickCanonicalExistingTestPath(candidates, lang)
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

// rankExistingTestPaths returns the candidate a plan item should extend.
//
// This used to be "first path under the canonical test tree, else paths[0]", with the list sorted
// lexicographically by buildExistingTestIndex. That tie-break is the second half of the duplicate
// artifact bug: when a SUT has both FooTest.java (left by an earlier ASQS run) and FooTests.java
// (the repository's real suite), BOTH sit under src/test/java, so the canonical check matched both
// and paths[0] decided — and '.' (0x2E) sorts before 's' (0x73), so the leftover always won. The
// repository's own suite was never extended again.
//
// The order below is a total order, most significant first. Layer is absent deliberately: callers
// filter by layer before reaching here (see existingTestPathsForItem), and re-deciding it at two
// levels is how the two got out of step in the first place.
//
//  1. under the language-canonical test tree
//  2. matches the repository's detected naming convention
//  3. human-authored in preference to ASQS-authored
//  4. more existing test methods — a real suite outranks a stub
//  5. lexicographic, purely so the result is deterministic
func rankExistingTestPaths(paths []string, lang string, conv TestSuffixConvention, generated genmanifest.Set) string {
	return rankExistingTestPathsInRepo(paths, lang, conv, generated, "")
}

// RankExistingTestPaths is the exported form of rankExistingTestPathsInRepo, for callers outside
// this package that must pick the same survivor this package would pick — notably duplicate-artifact
// reconciliation, which decides which of two colliding files to keep. Sharing the ranking is the
// point: a reconciler that chose differently would delete the file generation is about to extend.
func RankExistingTestPaths(paths []string, lang string, conv TestSuffixConvention, generated genmanifest.Set, repoRoot string) string {
	return rankExistingTestPathsInRepo(paths, lang, conv, generated, repoRoot)
}

// rankExistingTestPathsInRepo is rankExistingTestPaths with repoRoot available, which enables the
// test-method-count signal. repoRoot may be empty, in which case that signal is skipped.
func rankExistingTestPathsInRepo(paths []string, lang string, conv TestSuffixConvention, generated genmanifest.Set, repoRoot string) string {
	if len(paths) == 0 {
		return ""
	}
	cands := make([]existingTestCandidate, 0, len(paths))
	for _, p := range paths {
		cands = append(cands, existingTestCandidate{
			path:      p,
			canonical: underCanonicalTestTree(p, lang),
			matchesC:  conv.Detected() && unitTestSuffixOf(genmanifest.Normalize(p), lang) == conv.Suffix,
			// Unknown provenance counts as human-authored: refusing to rank a file we have no
			// record of would penalise every repository ASQS has never written to.
			human:   !generated.Has(p),
			methods: countTestMethods(repoRoot, p, lang),
		})
	}
	best := 0
	for i := 1; i < len(cands); i++ {
		if betterExistingTestCandidate(cands[i], cands[best]) {
			best = i
		}
	}
	return cands[best].path
}

type existingTestCandidate struct {
	path      string
	canonical bool
	matchesC  bool
	human     bool
	methods   int
}

// betterExistingTestCandidate reports whether a beats b under the documented total order.
func betterExistingTestCandidate(a, b existingTestCandidate) bool {
	if a.canonical != b.canonical {
		return a.canonical
	}
	if a.matchesC != b.matchesC {
		return a.matchesC
	}
	if a.human != b.human {
		return a.human
	}
	if a.methods != b.methods {
		return a.methods > b.methods
	}
	return a.path < b.path
}

// countTestMethods returns how many test methods rel declares, or 0 when it cannot be read. Used
// only as a late tie-break, so an unreadable file degrades to "no signal" rather than an error.
func countTestMethods(repoRoot, rel, lang string) int {
	if strings.TrimSpace(repoRoot) == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(genmanifest.Normalize(rel))))
	if err != nil {
		return 0
	}
	body := string(b)
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java":
		return strings.Count(body, "@Test")
	case "csharp", "cs":
		return strings.Count(body, "[Fact]") + strings.Count(body, "[Theory]") + strings.Count(body, "[Test]")
	case "javascript", "typescript", "js", "ts":
		return strings.Count(body, "it(") + strings.Count(body, "test(")
	}
	return 0
}

// underCanonicalTestTree reports whether p sits in the language's conventional test root.
func underCanonicalTestTree(p, lang string) bool {
	switch lang {
	case "java":
		return strings.Contains(filepath.ToSlash(p), "src/test/java/")
	case "javascript", "typescript", "js", "ts", "csharp", "cs":
		first := strings.SplitN(filepath.ToSlash(p), "/", 2)[0]
		for _, root := range layout.DedicatedRootDirCandidates {
			if strings.EqualFold(first, root) {
				return true
			}
		}
	}
	return false
}

// auditCapabilityDegraded records that a requested CompleteOptions field is not supported by the
// configured provider, so the fallback is a declared decision rather than an invisible one.
func (g *LLMGenerator) auditCapabilityDegraded(ctx context.Context, capability, detail string) {
	if g.Audit == nil {
		return
	}
	g.Audit.Log(ctx, "llm.capability_degraded", map[string]interface{}{
		"capability": capability,
		"detail":     detail,
	})
}
