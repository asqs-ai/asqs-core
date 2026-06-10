package retrieval

import "strings"

// IsE2EPlanItem is true when the plan item targets E2E-style gaps (layer or symbol kind).
// Used for context intros and for selecting the E2E vs unit-test output contract.
func IsE2EPlanItem(item *TestPlanItem) bool {
	if item == nil {
		return false
	}
	symKind := ""
	if item.Gap != nil && item.Gap.Symbol != nil {
		symKind = strings.TrimSpace(item.Gap.Symbol.Kind)
	}
	return strings.EqualFold(strings.TrimSpace(item.Layer), "e2e") ||
		symKind == "E2E_SPEC" || symKind == "PAGE_OBJECT" || symKind == "USER_FLOW" ||
		symKind == "API_ROUTE" || symKind == "PAGE_ROUTE"
}

const outputContractHeader = "## OUTPUT CONTRACT (mandatory — read last)\n\n"

// OutputContractBlock returns a short, fixed closing block for the assembled user context.
// It states the exact artifact type, forbidden alternatives (prose, wrong modality, JSON), and fence rules.
// Placed at the end of the user message to exploit recency: the model’s last instructions match the downstream parser (see also Liu et al., Lost in the Middle, on salient placement).
func OutputContractBlock(item *TestPlanItem, opts FormatOptions) string {
	if opts.DocGeneration {
		if IsE2EPlanItem(item) {
			return outputContractHeader +
				"Your reply must contain **only** in-file documentation appropriate to the language " +
				"(e.g. Java **Javadoc** `/** … */`, C# **XML** `///`, JS/TS **JSDoc/TSDoc** `/** … */`) for the **E2E-related** target above.\n\n" +
				"**Do not** output runnable **Playwright/Cypress/Jest/JUnit** test bodies, `describe`/`it`/`test(`/`expect`, or full spec files. " +
				"**Do not** wrap your entire answer in a single markdown ``` fence. **Do not** add conversational preamble. " +
				"**Do not** output JSON or YAML representing the documentation — emit **raw comment syntax** ready to insert above the symbol. " +
				"**Do not** wrap the comment block in XML envelope tags such as `<result>` or `</result>`.\n"
		}
		return outputContractHeader +
			"Your reply must contain **only** in-file documentation appropriate to the language " +
			"(Java **Javadoc** `/** … */`, C# **XML** `///`, JS/TS **JSDoc/TSDoc** `/** … */`).\n\n" +
			"**Do not** include runnable test code (`describe`, `it`, `test(`, `expect`, test classes). " +
			"**Do not** wrap your entire answer in markdown ``` fences. **Do not** add preamble (“Sure, here is…”). " +
			"**Do not** output JSON — only the comment block(s) to insert above the declaration. " +
			"**Do not** wrap the comment block in XML envelope tags such as `<result>` or `</result>`.\n"
	}
	if opts.UseStructuredTestJSON {
		if IsE2EPlanItem(item) {
			s := outputContractHeader +
				"Your **entire** reply must be **only** a single **JSON object**: keys = repo-relative **test or spec file path(s)**, values = **full file content** as strings (use `\\n` for newlines inside strings). " +
				"Use the **E2E stack** from the system message (Playwright, Cypress, Playwright Java, etc.).\n\n" +
				"**Do not** wrap the JSON in markdown ``` fences. **Do not** add preamble or explanation. " +
				"**Do not** use the **application/source** file path as a key — only the generated **test/spec** artifact path(s).\n"
			if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
				lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
				if lang == "javascript" || lang == "typescript" || lang == "js" || lang == "ts" {
					s += "**JavaScript/TypeScript:** Cypress vs Playwright APIs only as required by the stack—**not** a Jest/Vitest-only unit spec unless the existing file in context already mixes them.\n"
				}
			}
			return s
		}
		return outputContractHeader +
			"Your **entire** reply must be **only** a single **JSON object**: keys = repo-relative paths to the **test module(s)** you produce, values = **full file content** as strings (use `\\n` for newlines inside strings). " +
			"Match the **test framework** from the system message.\n\n" +
			"**Do not** wrap the JSON in markdown ``` fences. **Do not** add preamble. " +
			"**Do not** use **application/source** file paths as keys — only **test** artifact paths. " +
			"**Do not** replace behavioral tests with **string scans** of implementation files. " +
			"**Do not** output **tautological** tests (JS/TS: `expect(x).toBe(x)`, `expect(true).toBe(true)`; Java: `Assertions.assertTrue(true)` as the only assertion; C#: `Assert.True(true)` with no real check).\n" +
			"**Java/C#:** JSON keys must be **test** paths only (`src/test/java/...`, `*Tests.cs`)—never `src/main/java/...` or production `.cs` sources as keys.\n"
	}
	if IsE2EPlanItem(item) {
		s := outputContractHeader +
			"Your reply must contain **only** source code for an **end-to-end** or **integration** test using the **E2E stack** named in the system message and context (e.g. Playwright, Cypress, Playwright Java).\n\n" +
			"**Do not** respond with **unit-test-only** artifacts as the sole deliverable when an E2E flow is required. "
		if item != nil && item.Gap != nil && item.Gap.Symbol != nil {
			lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
			if lang == "javascript" || lang == "typescript" || lang == "js" || lang == "ts" {
				s += "**JavaScript/TypeScript:** For Cypress or Playwright targets, use that runner’s APIs only—**not** a Jest/Vitest unit spec (`jest.mock`, `vi.mock`, `@jest/globals` test/expect) unless the existing file in context already mixes them. "
			}
		}
		s += "**Do not** wrap the **entire** output in markdown ``` fences. **Do not** output JSON. " +
			"**Do not** paste the application source file with tests appended — emit **only** the test/spec source.\n"
		return s
	}
	return outputContractHeader +
		"Your reply must contain **only** source code for the **test file** (the generated test module or class), using the **test framework** from the system message and context.\n\n" +
		"**Do not** add long prose outside brief code comments. **Do not** wrap the **entire** file in markdown ``` fences (inline snippets in comments are fine). " +
		"**Do not** output JSON. **Do not** paste the whole **application** source with tests appended — emit **only** the **test** artifact. " +
		"**Do not** replace behavioral tests with **string scans** of implementation files unless the instructions above explicitly allow it. " +
		"**Do not** output **tautological** tests (JS/TS: `expect(x).toBe(x)`, `expect(true).toBe(true)`; Java: `Assertions.assertTrue(true)` only; C#: `Assert.True(true)` only).\n"
}
