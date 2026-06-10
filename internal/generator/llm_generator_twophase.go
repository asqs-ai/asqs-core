package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/llmfix"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// genMode selects how the system prompt is assembled for test generation.
type genMode int

const (
	genModeSingle genMode = iota
	genModePhase1
	genModePhase2
)

// TwoPhaseTestGeneration when true runs two LLM completions per unit-test gap: (1) skeleton
// (imports, containers, mock stubs, placeholder bodies), (2) full implementations conditioned on
// the skeleton. E2E items, extend-existing, and missing suggested paths skip two-phase.
// See docs/DOCUMENTATION.md — Two-phase test generation.
func (g *LLMGenerator) twoPhaseEligible(item *retrieval.TestPlanItem, contextStr, suggestedPath string, isE2E bool) bool {
	if !g.TwoPhaseTestGeneration || isE2E || item == nil {
		return false
	}
	if strings.TrimSpace(suggestedPath) == "" {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(contextStr), ExtendExistingTestContextPrefix) {
		return false
	}
	return true
}

func (g *LLMGenerator) buildGeneratorSystem(item *retrieval.TestPlanItem, isE2E bool, itemLang string, mode genMode) string {
	system := g.Prompt
	if system == "" {
		if isE2E {
			system = "You are an expert at writing end-to-end and integration tests. Generate only the requested artifact (spec, test class, or doc), no extra commentary. " +
				"Use the **E2E stack** named in this conversation (Playwright, Cypress, JUnit+Playwright Java, etc.) — do not substitute Jest/Vitest **unit-test** runner patterns for Cypress or Playwright specs."
		} else if mode == genModePhase1 {
			system = "You are an expert software engineer decomposing **unit test** authoring into steps (cf. least-to-most / subgoal decomposition). " +
				"**This is phase 1 of 2 — structure only:** emit a **compilable skeleton** for the dedicated test file: correct imports, outer test container (e.g. **JUnit 5** class under `src/test/java`, `describe`/`test` suite for JS/TS), " +
				"mock or module setup stubs (`jest.mock` / `vi.mock` / `@Mock` / `@ExtendWith(MockitoExtension.class)` / `@BeforeEach` with minimal bodies), and **named** test methods or `it`/`test` blocks whose **bodies are placeholders only** " +
				"(empty `{}`, `// TODO phase 2`, or a single temporary `expect(true).toBe(true)` / `Assertions.assertTrue(true)` **only** as a stub you will replace later — not as the final test). " +
				"Do **not** write full behavioral assertions in phase 1. For **Java**, tests live only under **`src/test/java`** (never in `src/main/java`). For JavaScript/TypeScript **unit** tests, the artifact is always a **separate** test module (*.test.* / *.spec.*), never the application source file."
		} else {
			system = "You are an expert at writing unit tests and documentation. Generate only the requested artifact (test class or doc), no extra commentary. " +
				"For **Java** **unit** tests, emit a **separate** class under **`src/test/java`** mirroring the **`src/main/java`** package layout (or the repo’s Gradle/Maven test tree from **Similar tests**)—never add `@Test` methods to production `.java` files. " +
				"For JavaScript/TypeScript **unit** tests, the artifact is always a **separate test module** (e.g. *.test.ts / *.spec.ts), never the application source file. " +
				"For **C#** **unit** tests, emit a **separate** `*Tests.cs` or `*Test.cs` file (same folder or project test convention as the repo)—never append test types into the production `.cs` file. " +
				"**Unit tests** must check **behavior** (invoke code, assert results, verify mocks)—not scan implementation source as text for expected snippets, and not tautologies like `expect(x).toBe(x)`, `expect(true).toBe(true)`, `Assertions.assertTrue(true)` with no real check, or C# `Assert.True(true)` / vacuous `Assert.Equal` on identical literals. " +
				"**C# specifically:** do not fill tests with **reflection-only** checks (`typeof(T).GetMethod`, `MethodInfo.GetParameters`, `Assert.NotNull(typeof(...))`) that only prove a member exists—use real calls, fakes, or Moq and assert **outcomes**. Prefer **in-memory/mocked `DbContext`** or fakes for database-heavy code unless **Similar tests** already use **WebApplicationFactory** + real SQL integration."
		}
	}
	// Skip unit-test contracts for JS/TS E2E (Cypress/Playwright vs Jest) and for C# E2E (Playwright/Selenium vs xUnit+Moq unit patterns).
	if g.ContractRules != nil && !(isE2E && (isJSTSLang(itemLang) || isCSharpLang(itemLang))) {
		c := *g.ContractRules
		system += "\n\nTest generation contract (" + c.Lang + "/" + c.Framework + "):\n"
		for _, r := range c.Rules {
			system += "- " + r + "\n"
		}
		if c.PreferPureUtilityFirst {
			system += "- Generate tests incrementally and conservatively; if dependencies are complicated, generate tests for pure/utility functions first."
		}
	}
	if isE2E {
		system = appendSkillPack(system, "E2E skill pack:", e2eSkillPack())
	} else if mode != genModePhase1 {
		system = appendSkillPack(system, "Unit testing skill pack:", unitSkillPack())
	}
	if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
		if isE2E {
			fw := strings.TrimSpace(g.E2EFramework)
			if fw == "" {
				fw = retrieval.DefaultE2EFrameworkForLang(itemLang)
			}
			if isJSTSLang(itemLang) {
				system += jsTSE2EGenerationContract(fw)
			}
			system += "\n\nThis item is **end-to-end** coverage. Stack: **" + fw + "**.\n" + e2ELLMHintForFramework(fw)
			if h := strings.TrimSpace(retrieval.E2EPromptCanonicalHints(itemLang, fw)); h != "" {
				system += "\n\n" + h
			}
			system += e2eGenerationActiveTestsPolicy()
		} else {
			if g.TestFramework != "" {
				fw := strings.TrimSpace(g.TestFramework)
				system += "\n\nThe project uses **" + fw + "** as its test framework. Generate tests using this framework only (e.g. Mocha: describe/it; Jest: describe/it/expect; Jasmine: describe/it/expect). Do not use Jest if the project uses Mocha, or vice versa."
			}
			lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
			switch lang {
			case "java":
				src := filepath.ToSlash(strings.TrimSpace(item.Gap.Symbol.File))
				system += "\n\n**Java — test file only:** Output a **complete** test class for the **suggested path** under `src/test/java` (same package as the type under test when mirroring Maven/Gradle defaults). " +
					"The path `" + src + "` is **main** source — do **not** embed `@Test` methods there. " +
					"Use **JUnit 5** (`@Test`, `Assertions`, `@ParameterizedTest` when appropriate) and **Mockito** unless **Similar tests** show JUnit 4 (`org.junit.Test`), TestNG, or Spock exclusively. " +
					"Prefer `org.junit.jupiter.api.Assertions` and `org.mockito.Mockito` / `@ExtendWith(MockitoExtension.class)` when that matches the repo. " +
					"If the target is **private** or depends on **hard-wired collaborators** (`new`-constructed services/clients, static singletons, direct DB clients), do **not** generate reflection/existence tests. " +
					"Instead, test the nearest **public behavior** that exercises that logic and isolate dependencies via mocks/fakes through existing seams."
			case "javascript", "typescript", "js", "ts":
				src := filepath.ToSlash(strings.TrimSpace(item.Gap.Symbol.File))
				system += "\n\n**JavaScript/TypeScript — test file only:** Output **only** the body of a **dedicated test file** (sibling *.test.* / *.spec.*, or __tests__ / e2e layouts that match the repo). " +
					"**Never** put `describe` / `it` / `test(` blocks into the **implementation** file — the path `" + src + "` is application source and must not receive generated test suites (no appending a second `describe` block below exports). " +
					"If the framework was not detected, infer Jest vs Vitest vs Mocha vs Jasmine from **Similar tests** and imports in the user context and stay consistent."
			case "csharp", "cs":
				src := filepath.ToSlash(strings.TrimSpace(item.Gap.Symbol.File))
				system += "\n\n**C# — test file only:** Output a **complete** test class in the **suggested test path** (typically parallel `*Tests.cs` / `*Test.cs`). " +
					"The path `" + src + "` is **production** code — do **not** embed `[Fact]` / `[Test]` methods inside that file. " +
					"Use **xUnit** (`Fact`, `Theory`, `Assert`), **NUnit**, or **MSTest** consistently with **Similar tests** and the project’s `.csproj` references. " +
					"If the target is **private** or depends on **hard-wired collaborators** (`new Storage(...)`, static singletons, direct DB clients), do **not** generate reflection/existence tests. " +
					"Instead, test the nearest **public behavior** that exercises that logic and isolate dependencies via mocks/fakes through existing seams."
			}
			if mode != genModePhase1 {
				system += behavioralUnitTestQualityHint(item)
				system += reactTSXUnitTestHint(item)
			} else {
				system += twoPhasePhase1LangHint(item)
			}
		}
	} else if g.TestFramework != "" {
		fw := strings.TrimSpace(g.TestFramework)
		system += "\n\nThe project uses **" + fw + "** as its test framework. Generate tests using this framework only (e.g. Mocha: describe/it; Jest: describe/it/expect; Jasmine: describe/it/expect). Do not use Jest if the project uses Mocha, or vice versa."
	}
	if mode == genModePhase2 {
		system += twoPhasePhase2SystemSuffix()
	}
	if !isE2E && mode != genModePhase1 {
		system += llmfix.GarbageTestAntiPatternsBlock
	}
	return system
}

func twoPhasePhase1LangHint(item *retrieval.TestPlanItem) string {
	if item == nil || item.Gap == nil || item.Gap.Symbol == nil {
		return ""
	}
	lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
	switch lang {
	case "java":
		return "\n\n**Phase 1 (Java):** JUnit 5 style: package, imports, test class, `@ExtendWith(MockitoExtension.class)` or rule setup if Mockito is idiomatic in context, `@BeforeEach void setUp()` may initialize mocks with `lenient()` or empty bodies. " +
			"Each `@Test void name()` method should have an **empty** body or only `// TODO phase 2` — no real assertions yet."
	case "javascript", "typescript", "js", "ts":
		return "\n\n**Phase 1 (JS/TS):** Include `describe`/`it` or `test` structure, `jest.mock`/`vi.mock` with minimal factory functions, and **stub** test bodies (comment or `expect(true).toBe(true)` **only** as placeholder)."
	case "csharp", "cs":
		return "\n\n**Phase 1 (C#):** xUnit/NUnit/MSTest: usings, test class, `[Fact]`/`[Test]` methods with `Assert.True(true)` or `throw new NotImplementedException()` as **temporary** placeholders only."
	default:
		return "\n\n**Phase 1:** Provide imports and outer test structure with **placeholder** inner bodies; no behavioral assertions yet."
	}
}

func twoPhasePhase2SystemSuffix() string {
	return "\n\n**Phase 2 of 2:** You are given your **phase-1 skeleton** in the conversation. **Replace** every placeholder, TODO, stub assertion, or empty test body with **real** behavioral tests. " +
		"Remove temporary `expect(true).toBe(true)`, `Assertions.assertTrue(true)`, or empty `@Test` bodies unless you intentionally keep a trivial sanity check with a comment — prefer meaningful assertions. " +
		"Do **not** replace placeholders with self-smoke tests (only constructing the test class), ctor-only NotNull-only checks, or empty skipped methods. Preserve structure and imports unless a change is required for compilation or correct mocking."
}

func twoPhaseUserPhase1Footer() string {
	return "\n\n---\n**Task — phase 1 of 2 (structure only):** Produce the test file **skeleton** as defined in the system message. Output format rules (e.g. JSON path→content) apply as usual."
}

func twoPhaseUserPhase2Footer(suggestedPath string) string {
	p := filepath.ToSlash(strings.TrimSpace(suggestedPath))
	return "\n\n---\n**Task — phase 2 of 2 (implement):** Using the skeleton in your previous reply, output the **complete** test file for **`" + p + "`** with full behavioral tests. Follow the same output format rules as phase 1."
}

// generateFromConversation runs one completion pass and normalizes output to file content.
func (g *LLMGenerator) generateFromConversation(ctx context.Context, messages []model.Message, suggestedPath string, useStructured bool) (content string, path string, err error) {
	structuredOn := useStructured
	completeOpts := func(structured bool) model.CompleteOptions {
		maxTok := 4096
		if structured || useStructured {
			maxTok = 8192
		}
		opts := model.CompleteOptions{MaxTokens: maxTok}
		if structured {
			opts.Structured = newGeneratedTestFilesStructuredSchema()
		}
		return opts
	}

	result, err := g.completeGenerateWithRetry(ctx, messages, completeOpts(structuredOn))
	if err != nil && structuredOn && llmfix.IsStructuredOutputAPIError(err) {
		structuredOn = false
		result, err = g.completeGenerateWithRetry(ctx, messages, completeOpts(false))
	}
	if err != nil {
		return "", "", err
	}

	raw := strings.TrimSpace(result.Content)
	if useStructured && structuredOn {
		if m, perr := llmfix.ParsePathContentMap(raw); perr == nil {
			if picked := pickGeneratedContentFromPathMap(m, suggestedPath); strings.TrimSpace(picked) != "" {
				return picked, suggestedPath, nil
			}
		}
		structuredOn = false
		result, err = g.completeGenerateWithRetry(ctx, messages, completeOpts(false))
		if err != nil {
			return "", "", err
		}
		raw = strings.TrimSpace(result.Content)
	}

	stripped := extractCodeBlockContent(raw)
	for _, cand := range []string{strings.TrimSpace(raw), stripped} {
		if m, perr := llmfix.ParsePathContentMap(cand); perr == nil {
			if picked := pickGeneratedContentFromPathMap(m, suggestedPath); strings.TrimSpace(picked) != "" {
				return picked, suggestedPath, nil
			}
		}
	}
	return stripped, suggestedPath, nil
}

func (g *LLMGenerator) generateTwoPhase(ctx context.Context, item *retrieval.TestPlanItem, contextStr string, isE2E bool, itemLang, suggestedPath string, useStructured bool) (content string, path string, err error) {
	sys1 := g.buildGeneratorSystem(item, isE2E, itemLang, genModePhase1)
	if useStructured {
		sys1 += structuredTestJSONSystemSuffix(suggestedPath)
	}
	user1 := contextStr + twoPhaseUserPhase1Footer()
	msg1 := []model.Message{
		{Role: "system", Content: sys1},
		{Role: "user", Content: user1},
	}
	raw1, _, err := g.generateFromConversation(ctx, msg1, suggestedPath, useStructured)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(raw1) == "" {
		return "", "", fmt.Errorf("llm generator: two-phase phase 1 produced empty content")
	}
	// Re-fetch full assistant payload for context: use trimmed raw from phase 1 completion by re-running pick only — we need the exact model string for multi-turn.
	// We stored parsed file content in raw1; for assistant message, re-encode as JSON if structured was used so phase 2 sees valid prior shape.
	assistantText := raw1
	if useStructured {
		key := filepath.ToSlash(strings.TrimSpace(suggestedPath))
		b, jerr := json.Marshal(map[string]string{key: raw1})
		if jerr != nil {
			return "", "", fmt.Errorf("llm generator: two-phase marshal phase-1 reply: %w", jerr)
		}
		assistantText = string(b)
	}

	sys2 := g.buildGeneratorSystem(item, isE2E, itemLang, genModePhase2)
	if useStructured {
		sys2 += structuredTestJSONSystemSuffix(suggestedPath)
	}
	msg2 := []model.Message{
		{Role: "system", Content: sys2},
		{Role: "user", Content: contextStr},
		{Role: "assistant", Content: assistantText},
		{Role: "user", Content: twoPhaseUserPhase2Footer(suggestedPath)},
	}
	return g.generateFromConversation(ctx, msg2, suggestedPath, useStructured)
}
