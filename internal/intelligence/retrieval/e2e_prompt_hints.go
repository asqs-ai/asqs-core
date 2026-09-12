package retrieval

import "strings"

// DefaultE2EFrameworkForLang returns a conservative default when DetectE2E / config left E2EFramework empty.
// JS/TS → playwright; Java → playwright-java; C# → playwright-dotnet; otherwise playwright.
func DefaultE2EFrameworkForLang(lang string) string {
	switch normalizeLangCode(lang) {
	case "javascript", "typescript":
		return "playwright"
	case "java":
		return "playwright-java"
	case "csharp":
		return "playwright-dotnet"
	default:
		return "playwright"
	}
}

func normalizeLangCode(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "cs":
		return "csharp"
	default:
		return l
	}
}

// E2EPromptCanonicalHints returns a short block of canonical imports and typical runner commands for the
// detected E2E stack, aligned with evaluator default E2E commands (npx playwright test, npx cypress run,
// Maven Failsafe / Gradle integrationTest, dotnet test ~E2E). Empty string if lang is unknown and fw is empty.
func E2EPromptCanonicalHints(lang, e2eFramework string) string {
	return E2EPromptCanonicalHintsForSurface(lang, e2eFramework, "")
}

// E2EPromptCanonicalHintsForSurface is E2EPromptCanonicalHints with the E2E surface bootstrap
// detected (none | api | ui | mixed; C# today).
//
// The surface changes what a correct E2E test even looks like. The hints described a browser test
// with Microsoft.Playwright for EVERY C# repository, so a Web API with no pages was asked for a test
// that cannot be written: nothing to navigate to, and the model's only options are to invent a UI or
// open about:blank. An `api` surface gets the shape that does test such an application —
// WebApplicationFactory<Program> with an HttpClient — and a `ui` surface keeps the browser guidance
// and names the base-URL convention a generated test needs to reach a running app.
//
// An empty surface means "not detected" and keeps the previous behaviour.
func E2EPromptCanonicalHintsForSurface(lang, e2eFramework, e2eSurface string) string {
	l := normalizeLangCode(lang)
	fw := strings.ToLower(strings.TrimSpace(e2eFramework))
	if fw == "" {
		fw = DefaultE2EFrameworkForLang(l)
	}

	var body string
	switch l {
	case "javascript", "typescript":
		body = e2eHintsJSTS(fw)
	case "java":
		body = e2eHintsJava(fw)
	case "csharp":
		body = e2eHintsCSharpForSurface(fw, e2eSurface, strings.TrimSpace(e2eFramework) != "")
	default:
		if strings.TrimSpace(e2eFramework) == "" {
			return ""
		}
		body = "**Stack:** " + strings.TrimSpace(e2eFramework) + " — use that framework’s official entry points and the repo’s existing scripts; match **Similar tests** in context when they use the same stack."
	}

	if body == "" {
		return ""
	}
	return "**E2E framework grounding (canonical imports & runner):**\n\n" + body
}

func e2eHintsJSTS(fw string) string {
	switch fw {
	case "cypress":
		return "- **Canonical API:** Mocha-style **`describe` / `it`** and the global **`cy`** chain (`cy.visit`, `cy.get`, `cy.contains`, `should`, …). Do **not** import **`@playwright/test`** or use Playwright’s `page` / `test` / `expect` from that package in Cypress specs.\n" +
			"- **TypeScript (optional):** `/// <reference types=\"cypress\" />` at the top when the project does not already expose Cypress globals via `types` in **tsconfig**.\n" +
			"- **Typical runner (CI):** `npx cypress run` — same default family as **`evaluator.resolveE2ETestCommand`** when **`runner.e2e_test_command`** is unset and **`E2EFramework` is `cypress`. Prefer an existing **`package.json`** script (e.g. `npm run e2e`) when it is clearly the Cypress entrypoint.\n" +
			"- **Reference:** Cypress testing fundamentals — [https://docs.cypress.io/app/core-concepts/writing-and-organizing-tests](https://docs.cypress.io/app/core-concepts/writing-and-organizing-tests)"
	default:
		// playwright and unknown JS/TS stacks default to Playwright test runner vocabulary
		return "- **Canonical import line:** `import { test, expect } from '@playwright/test';` — use **`test`**, **`test.describe`**, and **`expect`** from this package for assertions in this artifact; do **not** use Jest/Vitest **`expect`** from `@jest/globals` or `vitest` unless an **existing** spec in context already mixes them.\n" +
			"- **Typical runner (CI):** `npx playwright test` — matches **`evaluator.resolveE2ETestCommand`** default for **`playwright`**. If **`package.json`** defines **`test:e2e`** or similar wrapping Playwright, prefer that when it is the repo standard.\n" +
			"- **Reference:** Playwright Test — [https://playwright.dev/docs/writing-tests](https://playwright.dev/docs/writing-tests)"
	}
}

func e2eHintsJava(fw string) string {
	switch fw {
	case "selenium":
		return "- **Canonical imports (illustrative):** `import org.openqa.selenium.WebDriver;` `import org.openqa.selenium.By;` `import org.openqa.selenium.support.ui.WebDriverWait;` plus JUnit Jupiter **`org.junit.jupiter.api.*`** when the project uses JUnit 5.\n" +
			"- **Typical runner:** Maven **`mvn -B failsafe:integration-test`** or **`./mvnw -q -B failsafe:integration-test`**; Gradle **`gradle integrationTest`** or **`./gradlew integrationTest`** — browser tests belong in **integration** / **failsafe** scope, not surefire unit runs.\n" +
			"- **Reference:** Selenium WebDriver — [https://www.selenium.dev/documentation/webdriver/](https://www.selenium.dev/documentation/webdriver/)"
	case "selenide":
		return "- **Canonical imports:** `import static com.codeborne.selenide.Selenide.*;` `import static com.codeborne.selenide.Condition.*;` (and Selenide configuration as used in the repo).\n" +
			"- **Typical runner:** Same as Selenium-backed suites: Maven **Failsafe** or Gradle **`integrationTest`**.\n" +
			"- **Reference:** Selenide — [https://selenide.org/](https://selenide.org/)"
	default:
		return "- **Canonical imports (illustrative):** `import com.microsoft.playwright.*;` — create **`Playwright`**, **`Browser`**, **`Page`**, use **`page.locator(...)`** and Playwright Java assertions (**`assertThat`**) per the Playwright Java API.\n" +
			"- **Typical runner:** Maven **`mvn -B failsafe:integration-test`** or **`./mvnw -q -B failsafe:integration-test`**; Gradle **`integrationTest`** — aligned with **`defaultJavaE2EShellCommand`** in the evaluator when **`E2EFramework`** is **`playwright-java`** (or Selenium family).\n" +
			"- **Reference:** Playwright for Java — [https://playwright.dev/java/docs/intro](https://playwright.dev/java/docs/intro)"
	}
}

func e2eHintsCSharp(fw string) string {
	switch fw {
	case "playwright":
		return "- **Canonical import line:** `import { test, expect } from '@playwright/test';` — this repo’s browser E2E uses **Node Playwright** (e.g. **playwright.config.ts** at the root), not **Microsoft.Playwright** on .NET.\n" +
			"- **Typical runner:** `npx playwright test` — aligned with **`evaluator.resolveE2ETestCommand`** when **`E2EFramework`** is **`playwright`** for a **C#-indexed** polyglot layout.\n" +
			"- **Reference:** Playwright Test — [https://playwright.dev/docs/writing-tests](https://playwright.dev/docs/writing-tests)"
	case "selenium":
		return "- **Canonical imports:** `using OpenQA.Selenium;` `using OpenQA.Selenium.Support.UI;` (and the project’s chosen test SDK: xUnit, NUnit, or MSTest).\n" +
			"- **Typical runner:** `dotnet test` (optionally with a filter matching E2E test names if the repo uses that convention).\n" +
			"- **Reference:** Selenium C# — [https://www.selenium.dev/documentation/webdriver/getting_started/install_library/](https://www.selenium.dev/documentation/webdriver/getting_started/install_library/)"
	default:
		return "- **Canonical imports:** `using Microsoft.Playwright;` — obtain **`IPlaywright`**, **`IBrowser`**, **`IPage`** via the async Playwright .NET API; match the repo’s test adapter (**NUnit**, **MSTest**, or **xUnit**) and assertion helpers from Playwright’s docs for that adapter.\n" +
			"- **Typical runner:** `dotnet test -c Release --filter \"FullyQualifiedName~E2E\"` — same **heuristic** as **`defaultCSharpE2EShellCommand`** when **`runner.e2e_test_command`** is unset; override in config when the repo uses different naming.\n" +
			"- **Reference:** Microsoft.Playwright for .NET — [https://playwright.dev/dotnet/docs/intro](https://playwright.dev/dotnet/docs/intro)"
	}
}

// e2eHintsCSharpForSurface adds the surface dimension to the framework one. A framework the repo
// already uses always wins: a Selenium solution gets Selenium guidance whatever its pages look like.
func e2eHintsCSharpForSurface(fw, surface string, frameworkExplicit bool) string {
	if frameworkExplicit {
		return e2eHintsCSharp(fw)
	}
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case "api":
		return csharpAPIE2EHints
	case "ui", "mixed":
		return csharpUIE2EHints
	default:
		return e2eHintsCSharp(fw)
	}
}

// csharpAPIE2EHints describes an in-process HTTP E2E test: no browser, no server to start, and the
// real middleware pipeline. This is what an ASP.NET Core API's end-to-end test is.
const csharpAPIE2EHints = "- **This application has no browser-drivable pages** (detected E2E surface: `api`). Write an in-process HTTP end-to-end test, not a browser test.\n" +
	"- **Canonical imports:** `using Microsoft.AspNetCore.Mvc.Testing;` `using System.Net.Http.Json;` — derive from **`WebApplicationFactory<Program>`** (or inject it via `IClassFixture<WebApplicationFactory<Program>>`), call **`CreateClient()`** for an **`HttpClient`** bound to the in-memory server, and assert on the real pipeline: status code, headers, and the deserialised body.\n" +
	"- **Top-level statements:** `Program` is internal unless the web project declares `public partial class Program { }`. When it does not, use the entry-point type the project does expose rather than inventing one.\n" +
	"- **Typical runner:** `dotnet test -c Release --filter \"FullyQualifiedName~E2E\"` — the same heuristic as **`defaultCSharpE2EShellCommand`** when **`general.build.e2e_test_command`** is unset.\n" +
	"- **Reference:** Integration tests in ASP.NET Core — [https://learn.microsoft.com/aspnet/core/test/integration-tests](https://learn.microsoft.com/aspnet/core/test/integration-tests)"

// csharpUIE2EHints describes a Playwright .NET browser test against a running application. The
// base URL is an environment variable because Playwright .NET has no webServer block: unlike the
// JS config ASQS writes, nothing in the .NET stack starts the app, so the runner does.
const csharpUIE2EHints = "- **This application serves browser-drivable pages** (detected E2E surface: `ui`/`mixed`).\n" +
	"- **Canonical imports:** `using Microsoft.Playwright;` — obtain **`IPlaywright`**, **`IBrowser`**, **`IPage`** through the async Playwright .NET API, one `IPage` per test; match the repo's test adapter (**xUnit**, **NUnit** or **MSTest**).\n" +
	"- **Base URL:** read **`ASQS_BASE_URL`** from the environment and navigate to paths relative to it (`await page.GotoAsync($\"{baseUrl}/orders\")`). Never navigate to a `data:` URL and never hard-code `http://localhost:5000`: the port is assigned per run.\n" +
	"- **Selectors:** address elements by the `data-testid` attributes the page already carries (`page.GetByTestId(\"orders-search\")`), not by CSS structure or visible text.\n" +
	"- **Typical runner:** `dotnet test -c Release --filter \"FullyQualifiedName~E2E\"`.\n" +
	"- **Reference:** Playwright for .NET — [https://playwright.dev/dotnet/docs/intro](https://playwright.dev/dotnet/docs/intro)"
