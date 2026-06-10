// Package contract defines per-language test generation contracts.
// These rules guide generation and evaluation: style, mocking, and incremental/conservative constraints.
package contract

// Contract is the test generation contract for a language/framework.
// Used by generators and evaluators to enforce consistency and correctness.
type Contract struct {
	Lang      string   // e.g. "java", "csharp"
	Framework string   // e.g. "junit5", "xunit"
	Rules     []string // human-readable rules for generation
	// IncrementalConstraint: generate tests for pure/utility functions first when dependencies are complicated.
	PreferPureUtilityFirst bool
}

// JavaJUnit5 returns the contract for Java with JUnit 5.
func JavaJUnit5() Contract {
	return Contract{
		Lang:                   "java",
		Framework:              "junit5",
		PreferPureUtilityFirst: true,
		Rules: []string{
			"Do not generate tests for private methods; only test public (and protected) API",
			"Emit **standalone** test classes under **`src/test/java`** (mirror package of **`src/main/java`** for Maven/Gradle defaults unless **Similar tests** show another layout)—never add `@Test` methods to `src/main/java`",
			"Prefer constructor + dependency injection patterns used in repo",
			"Mocking: Mockito if present; otherwise minimal fakes",
			"One test file per class under test, small focused `@Test` methods",
			"**Meaningful tests:** assert return values, exceptions, and `verify(...)` on mocks—not empty methods, not `assertTrue(true)`, not `assertEquals` with the same literal twice as the sole check",
			"**Forbidden:** `Files.readString`/`readAllLines` on the implementation `.java` + substring assertions as the main proof of behavior",
			"Assert observable behavior (return values, exceptions, mock interactions); do not write tests that only scan source files or assert that code text contains expected substrings",
			"**Forbidden garbage:** no self-smoke tests (e.g. assertNotNull(new MyTestClass())), no ctor-only tests with only assertNotNull(new Foo()) and no API calls, no new empty @Disabled / @Ignored methods used as placeholders",
		},
	}
}

// CSharpXUnit returns the contract for C# with xUnit (.NET).
func CSharpXUnit() Contract {
	return Contract{
		Lang:                   "csharp",
		Framework:              "xunit",
		PreferPureUtilityFirst: true,
		Rules: []string{
			"Do not generate tests for private methods; only test public (and protected) API",
			"Emit a **standalone** test file (`*Tests.cs` / `*Test.cs` or project test layout)—never add test types to production `.cs` files",
			"Use xUnit if present; otherwise MSTest/NUnit based on repo and **Similar tests**",
			"When the test project uses **xUnit** (e.g. `PackageReference` **xunit** plus optional helpers like **Zoid.xUnit.Test**), use normal xUnit attributes and APIs in namespace **`Xunit`** (`[Fact]`, `[CollectionDefinition]`, fixtures, etc.) and add **`using Xunit;`** as needed—mirror existing tests in the repo rather than inventing framework-specific glue",
			"Mocking: Moq if present; otherwise hand-rolled fakes",
			"Respect nullable reference types settings if enabled",
			"**Meaningful tests:** Each `[Fact]` / `[Test]` must **execute** production code (construct types, call methods, await tasks) and assert **observable results**—return values, thrown exceptions, collection contents, or **Moq `Verify`** / callback arguments—not an empty body, not `Assert.True(true)`, not `Assert.Equal(x, x)` with literals",
			"**Forbidden — reflection / metadata-only tests:** Do not “test” by introspecting signatures alone, e.g. `typeof(T).GetMethod(...)`, `MethodInfo.GetParameters()`, `Assert.NotNull(typeof(SomeInterface))`, or `BindingFlags` probes that only prove a member exists. That duplicates the compiler and does not validate behavior.",
			"**Instead:** For interfaces or DI-bound types, use **hand-written fakes** or **Moq** `Mock<T>` with setups, call the **concrete** class or system under test that consumes the interface, then assert on **returned data** or **verified** calls. If the target is a **record/DTO** or trivial type, test meaningful factories, mapping, or validation logic—not `Assert.NotNull` on `typeof`.",
			"**Forbidden:** `File.ReadAllText`/similar on the implementation file + `Contains` on source text as the main check—that is not a behavioral test",
			"**Database / EF Core:** Prefer **in-memory doubles**, **`UseInMemoryDatabase`**, **SQLite :memory:**, or **mocked `DbContext`/repositories** when exercising logic—only mirror **full integration WebApplicationFactory + SQL** patterns when **Similar tests** already do and the sandbox can supply configuration",
			"**Forbidden garbage:** no tests that only Assert.NotNull(new ThisTestClass()), no ctor-only [Fact] with only new + NotNull, no new empty [Fact(Skip)] / [Ignore] bodies as shipped tests",
		},
	}
}

// JavaScriptDetected returns the contract for JavaScript: use the project's detected test framework.
func JavaScriptDetected() Contract {
	return Contract{
		Lang:                   "javascript",
		Framework:              "detected",
		PreferPureUtilityFirst: true,
		Rules: []string{
			"Use the project's detected test framework only (see system prompt and context for the framework name)",
			"Follow existing test style and assertion library in the repo",
			"Mocha: describe/it; Jest/Vitest: describe/it/expect; Jasmine: describe/it/expect",
			"Emit only a standalone test file body (*.test.* / *.spec.* or repo test layout); never embed describe/it/test suites in non-test application source files",
			"**Meaningful tests:** Import or invoke the function/class under test and assert return values, rejections, and side effects. Mock modules/globals (jest.mock, vi.mock, sinon, etc.) so I/O and frameworks (e.g. Strapi entityService, Prisma) return controlled data",
			"**Forbidden — vacuous assertions:** Do not write tests whose expectations are tautologies, e.g. `expect(true).toBe(true)`, `expect(1).toBe(1)`, `expect('./foo').toBe('./foo')`, or comparing two identical string literals/constants. Every assertion must depend on **runtime output** from code you invoked or **DOM** from a render, or on **mock** call counts/arguments",
			"**React / .tsx UI:** Prefer `@testing-library/react`: `render()` the component (wrap with providers from context if needed), query with `screen` (role, label, text), `userEvent` or `fireEvent`, and assert what the user sees or which collaborators were called. Mock `next/navigation`, routers, and data hooks when the implementation uses them — do not substitute path-string self-comparisons for real UI or behavior checks",
			"**Forbidden:** fs.readFileSync/readFile on the implementation path + expect(string).toContain/match on source text — that only checks copy-paste, not behavior, and is not acceptable",
			"**Forbidden garbage:** no it/test that only expects the test file or wrapper to be defined without calling production code; no ctor-only expect(new Foo()).toBeDefined() as the sole check; no empty it.skip/test.skip shipped as the final artifact",
			"Prefer 2–4 focused cases: happy path, empty/no results, error path, and (when relevant) that collaborators were called with expected arguments",
		},
	}
}

// TypeScriptDetected returns the contract for TypeScript: same as JavaScript but Lang set for LLM.
func TypeScriptDetected() Contract {
	c := JavaScriptDetected()
	c.Lang = "typescript"
	return c
}

// ByLang returns the contract for the given language (Java JUnit 5, C# xUnit, JS/TS detected framework).
func ByLang(lang string) Contract {
	switch lang {
	case "csharp", "cs":
		return CSharpXUnit()
	case "typescript", "ts":
		return TypeScriptDetected()
	case "javascript", "js":
		return JavaScriptDetected()
	default:
		return JavaJUnit5()
	}
}
