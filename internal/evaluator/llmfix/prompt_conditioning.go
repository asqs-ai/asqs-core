package llmfix

import (
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// augmentFixSystemPrompt appends step- and attempt-conditioned instructions after the base system text
// (default or custom). This implements task-conditioned prompting: the model sees different role emphasis
// for compile vs test vs E2E, and stronger guidance on later fix attempts.
func augmentFixSystemPrompt(base string, req evaluator.FixRequest) string {
	base = strings.TrimSpace(base)
	var parts []string
	if base != "" {
		parts = append(parts, base)
	}
	if s := fixStepConditioningBlock(req); s != "" {
		parts = append(parts, s)
	}
	if s := fixAttemptConditioningBlock(req.FixAttempt, req.MaxFixAttempt); s != "" {
		parts = append(parts, s)
	}
	if s := fixSkillPackBlock(req); s != "" {
		parts = append(parts, s)
	}
	if s := fixAccessModifierSkillPackBlock(req); s != "" {
		parts = append(parts, s)
	}
	if s := fixCSharpManifestAndReferenceBlock(req.Lang); s != "" {
		parts = append(parts, s)
	}
	if s := fixCSharpTestRepairQualityBlock(req); s != "" {
		parts = append(parts, s)
	}
	if s := fixInfrastructureLanguageRepairBlock(req); s != "" {
		parts = append(parts, s)
	}
	if s := fixTestGarbageAntiPatternsBlock(req); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// fixTestGarbageAntiPatternsBlock applies on unit and E2E test fix turns for every language.
func fixTestGarbageAntiPatternsBlock(req evaluator.FixRequest) string {
	switch req.Step {
	case evaluator.StepTest, evaluator.StepTestE2E:
		base := strings.TrimSpace(GarbageTestAntiPatternsBlock)
		if k := strings.TrimSpace(req.InfrastructureFailureKind); k != "" {
			base += "\n\n### Infrastructure classification (evaluator)\n" +
				"This failure was classified as environment/infrastructure (`" + k + "`). Conditional skips or guards are permitted **only** when needed for that scenario—still avoid unrelated hollow tests.\n"
		}
		return base
	default:
		return ""
	}
}

// fixCSharpTestRepairQualityBlock applies on C# unit (StepTest) and E2E (StepTestE2E) fix turns: forbid fixes that
// only silence failures by hollowing out tests or replacing them with placeholders.
func fixCSharpTestRepairQualityBlock(req evaluator.FixRequest) string {
	switch strings.ToLower(strings.TrimSpace(req.Lang)) {
	case "csharp", "cs":
	default:
		return ""
	}
	switch req.Step {
	case evaluator.StepTest, evaluator.StepTestE2E:
	default:
		return ""
	}
	var infraPre string
	if k := strings.TrimSpace(req.InfrastructureFailureKind); k != "" {
		infraPre = "### Classified infrastructure failure (" + k + ")\n" +
			"The evaluator classified this output as missing or invalid environment configuration (database connection string, DB host, etc.). " +
			"You MAY narrow or skip **only** the failing test method using repo-consistent conditional/xUnit skip patterns from **Similar tests**, without degrading unrelated tests.\n\n"
	}
	return infraPre + "### C# unit / E2E tests: do not fix by degrading the test\n" +
		"- **Forbidden as a fix:** empty test methods; bodies that only `return;` or contain no real check; placeholder comments like \"TODO\" or \"fix later\" instead of assertions; replacing a failing case with a no-op.\n" +
		"- **Forbidden:** replacing a failing behavioral test with **reflection-only** surface checks (`Type.GetMethod`, `GetParameters`, `Assert.NotNull(typeof(...))` without invoking real logic)—that is not a repair, it removes coverage.\n" +
		"- **Forbidden:** type-metadata-only checks as a substitute for behavior, e.g. asserting `typeof(Program).Namespace`, `typeof(Startup).Namespace`, `typeof(Foo).Name`, or `var t = typeof(Foo); Assert.IsNotNull(t)` without exercising production behavior.\n" +
		"- **Forbidden:** constructor-only null-guard smoke tests (e.g. only `Assert.Throws<ArgumentNullException>(() => new Foo(..., null))`) when used to replace broader failing behavior tests.\n" +
		"- **Forbidden:** adding or keeping **new** empty `[Fact(Skip)]` / `[Theory(Skip)]` / `[Ignore]` methods with no body as a substitute for a real fix (unless one existing repo pattern documents a single integration-only skip—never multiple hollow stubs).\n" +
		"- **Forbidden:** dummy or tautological assertions that always pass, e.g. `Assert.True(true)`, `Assert.False(false)`, `Assert.Equal(1, 1)`, or `Assert.Equal` / `Assert.Same` with the **same** literal or variable on both sides as the **only** meaningful check.\n" +
		"- **Forbidden:** deleting or commenting out most assertions, or shrinking the test to a single vacuous line, just to get green builds.\n" +
		"- **Forbidden — bait-and-switch:** replacing an **integration** or **database/fixture-backed** test (e.g. custom `[IntegrationFact]`, `IClassFixture`, storage/SQL, Testcontainers) with an **unrelated** shallow test (in-memory dictionaries, string constants, or logic that does **not** exercise the same system under test) just to make the step pass. That destroys coverage and is worse than skipping.\n" +
		"- **Do** fix the underlying issue when possible: correct types/usings, async/await and synchronization, mocks/stubs that match real APIs, Playwright/Selenium selectors and waits, xUnit/NUnit/MSTest API usage, or data setup so the **original intent** of the test still runs and asserts observable behavior.\n" +
		"- **Integration / dependency-bound failures:** when the failure is clearly due to **missing evaluation environment** (no real DB, container, network, secrets, or fixture lifecycle) and you cannot faithfully run the same test here, **do not** invent a substitute fake test. Prefer **disabling only that one test method** using the **same skip/disable pattern the repo already uses** (e.g. xUnit `[Fact(Skip = \"…\")]` / `[Theory(Skip = \"…\")]`, or a custom attribute like `[IntegrationFact(Skip = \"…\")]` if that exists in **Similar tests**), with a **one-line honest reason** (e.g. requires live SQL / integration host). **Leave all other tests in the file unchanged** — no collateral hollowing or unrelated rewrites.\n" +
		"- For **unit** tests that do not require external infra, still prefer a real fix over skip; use skip/ignore only as a **last** resort and never degrade unrelated methods in the same file."
}

// fixInfrastructureLanguageRepairBlock adds infra-specific repair guidance for JVM and JS/TS when the
// evaluator classified the failure (parity with the C# infra preamble inside fixCSharpTestRepairQualityBlock).
func fixInfrastructureLanguageRepairBlock(req evaluator.FixRequest) string {
	k := strings.TrimSpace(req.InfrastructureFailureKind)
	if k == "" {
		return ""
	}
	switch req.Step {
	case evaluator.StepTest, evaluator.StepTestE2E:
	default:
		return ""
	}
	lang := strings.ToLower(strings.TrimSpace(req.Lang))
	switch lang {
	case "java", "kotlin", "scala":
		return "### Classified infrastructure failure (" + k + ") — JVM tests\n" +
			"The evaluator classified this output as missing or invalid environment configuration (JDBC URL, DB host, credentials). " +
			"You MAY narrow or skip **only** the failing test method using repo patterns from **Similar tests**: JUnit 5 `@Disabled` with an honest reason, JUnit 4 `@Ignore`, TestNG `enabled=false`, or `org.junit.jupiter.api.Assumptions.assume*` / `org.junit.Assume.assume*` guards when the repo already uses them—without hollowing unrelated tests."
	case "javascript", "typescript", "js", "ts":
		return "### Classified infrastructure failure (" + k + ") — JS/TS tests\n" +
			"The evaluator classified this output as missing or invalid environment configuration (DB URL, Docker service, etc.). " +
			"You MAY narrow or skip **only** the failing test using repo patterns from **Similar tests**: conditional `describe`/`it.skip`/`test.skip`, `describe.skip`, or environment guards—without degrading unrelated tests."
	default:
		return ""
	}
}

func fixCSharpManifestAndReferenceBlock(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
	default:
		return ""
	}
	return "### C# / .NET: manifests are authoritative (included *.csproj, optional Directory.Packages.props / Directory.Build.props)\n" +
		"The user message may include the **test project .csproj** for the failing file(s) plus shared MSBuild props. Treat those files as the source of truth for **PackageReference** and **ProjectReference** assemblies.\n" +
		"- **Do not** add new `<PackageReference>` / `<ProjectReference>` lines (you only edit test `.cs` artifacts).\n" +
		"- **xUnit** types ([Fact], [Theory], [CollectionDefinition], IClassFixture<>, etc.) live in the **Xunit** namespace — add **using Xunit;** when you use them **if** the included `.csproj` (or central props) shows **xunit** / **xunit.core** / a meta-package that clearly brings xUnit. If xUnit is referenced but the compiler still cannot resolve an attribute, fix **usings** and attribute names before inventing replacements.\n" +
		"- Repos often combine **xUnit** with custom test helper packages (e.g. **Zoid.xUnit.Test**): follow **imports and patterns from Similar tests / existing tests** in the repo; do not paste generic snippets that need packages not listed in the manifests.\n" +
		"- If a type truly is not covered by the listed references, **remove** that pattern and rewrite using only APIs implied by the manifests and existing repo tests—do not assume it should compile without evidence in the .csproj."
}

// fixStepConditioningBlock narrows the fixer's objective to the failing sandbox step (compile,
// test, or E2E). For StepTest failures whose log is actually compiler-shaped (javac/kotlinc/csc/
// tsc), the block is redirected to fixTestPhaseCompileBlock so the LLM is not told to "fix
// assertions and mocks" when the real blocker is a missing symbol or type mismatch in the test
// artifact itself.
func fixStepConditioningBlock(req evaluator.FixRequest) string {
	switch req.Step {
	case evaluator.StepCompile:
		return `### Step-conditioned role: COMPILE/BUILD failure
Treat the log as compiler or static build output. Prioritize: correct packages and imports (only from manifests), type and signature mismatches, symbols resolvable in-repo, and errors visible to the build tool. Do not spend the first fix on test-only assertion style unless the compile error is clearly caused by syntax or imports inside the test artifact.`
	case evaluator.StepTest:
		if errout.IsCompileShaped(req.ErrorOutput) {
			return fixTestPhaseCompileBlock(req)
		}
		return `### Step-conditioned role: TEST execution failure
Treat the log as unit or integration test output (failed assertions, exceptions, timeouts). Prioritize: mocks and stubs that match real APIs in dependency/source files, correct assertion APIs for the detected framework, async/act boundaries, fixtures, and setup/teardown. Prefer behavioral checks over tautological expectations.`
	case evaluator.StepTestE2E:
		return `### Step-conditioned role: E2E / browser failure
Treat the log as browser or UI automation output. Prioritize: selectors, navigation, waiting/timing strategies, test isolation, and framework-specific APIs. Separate likely flakiness (timing) from genuine application errors when the log allows.`
	default:
		return ""
	}
}

// fixTestPhaseCompileBlock is the hybrid block emitted when the StepTest log is compile-shaped
// (javac / kotlinc / csc / tsc error codes, `cannot find symbol`, `package ... does not exist`,
// etc.). It borrows the compile-role wording from StepCompile but adds a language-aware degradation
// nudge so the fixer does not "repair" a missing source symbol by hollowing the test. This is the
// routing the plan calls out as improvement D — StepTest compile-shape detection.
func fixTestPhaseCompileBlock(req evaluator.FixRequest) string {
	const header = "### Step-conditioned role: TEST-PHASE COMPILE failure\n" +
		"The failing stage reports as TEST but the output is a compiler diagnostic (e.g. `cannot find symbol`, `no suitable method found`, `incompatible types`, `error CSxxxx`, `TSxxxx`). Treat the log as a compiler report: fix types, method/field signatures, and imports **in the test file(s) listed as writable**. Do not rewrite assertions, mocks, or the test shape to sidestep a missing symbol — that is a degradation, not a repair.\n" +
		"- First check whether the missing symbol is supposed to exist on a dependency/source class shown in read-only context. If yes, adjust the **test** to call the correct existing API (right method name, argument types, static vs instance); do not fabricate a non-existent method.\n" +
		"- If the symbol really does not exist in the codebase at all and is not importable from any manifest, the test may be against an imagined API. Rewrite the test to use the closest real API visible in dependency/source files; never remove the behavioral intent by replacing it with a tautology or an empty body.\n" +
		"- Keep the fix minimal: import corrections, type coercions, and method-call fixes are preferred over structural test rewrites."
	switch strings.ToLower(strings.TrimSpace(req.Lang)) {
	case "java", "kotlin", "scala":
		return header + "\n" +
			"- For JVM tests, verify that any new import you add is satisfied by the included `pom.xml` / `build.gradle` / `build.gradle.kts` entries. Prefer adjusting the test call site over inventing a new dependency.\n" +
			"- A Java `cannot find symbol` with a `location: ... of type <FQCN>` hint almost always means the test is calling a method that does not exist on that class. Read that class's source (it is included) and pick the real accessor name/return type."
	case "csharp", "cs":
		return header + "\n" +
			"- For C# tests, the `.csproj` and any `Directory.Packages.props` / `Directory.Build.props` in context are authoritative for `PackageReference` / `ProjectReference`. Fix `using` statements and type/method references to match what those manifests actually bring in; do not add new package references (you can only edit `.cs` test files)."
	case "typescript", "ts", "javascript", "js":
		return header + "\n" +
			"- For TS/JS tests, the `package.json` / `tsconfig.json` in context list the real dependencies and compile options. Fix imports, generic parameters, and function signatures against the types those packages actually export."
	default:
		return header
	}
}

// fixAttemptConditioningBlock adds retry-specific guidance; later attempts bias toward import/manifest
// verification and then toward mocks/assertions simplification (aligned with iterative repair practice).
func fixAttemptConditioningBlock(attempt, maxAttempt int) string {
	if attempt < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Retry guidance (fix attempt ≥ 2)\n")
	b.WriteString("- Re-verify every import, package name, and module path against the manifests; fix typos and wrong coordinates before larger rewrites.\n")
	b.WriteString("- Prefer the smallest change that addresses the current error message; avoid unrelated refactors.\n")
	late := attempt >= 3 || (maxAttempt > 0 && attempt >= maxAttempt)
	if late {
		b.WriteString("\n### Late-attempt bias (attempt ≥ 3 or final attempt in this step)\n")
		b.WriteString("- Strongly align mocks, stubs, and expect()/assert* calls with signatures and behavior visible in dependency/source files.\n")
		b.WriteString("- If the same failure class repeats, simplify: fewer nested expectations, explicit framework-native waits, and minimal stable assertions.\n")
	}
	return strings.TrimSpace(b.String())
}

// fixUserTurnFocusBlock is a short user-side recap (recency in long prompts; complements the system addenda).
func fixUserTurnFocusBlock(req evaluator.FixRequest) string {
	var b strings.Builder
	b.WriteString("=== TURN FOCUS ===\n")
	switch req.Step {
	case evaluator.StepCompile:
		b.WriteString("Failure stage: COMPILE/BUILD — fix types, imports, and build-visible issues first.\n")
	case evaluator.StepTest:
		b.WriteString("Failure stage: TEST — fix assertions, mocks, and framework/runtime setup to match production APIs.\n")
	case evaluator.StepTestE2E:
		b.WriteString("Failure stage: E2E — fix selectors, navigation, timing, and browser/framework APIs.\n")
	default:
		b.WriteString("Address the reported step's tool output with minimal, evidence-based edits.\n")
	}
	// Metadata already lists fix_attempt; add a recency nudge only on retries (multi-turn or re-run loop).
	if req.FixAttempt >= 2 && req.MaxFixAttempt > 0 {
		b.WriteString(fmt.Sprintf("Prior fixes did not clear this step — adopt a different root-cause hypothesis (attempt %d of %d).\n", req.FixAttempt, req.MaxFixAttempt))
	}
	return b.String()
}
