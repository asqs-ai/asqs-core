package llmfix

// GarbageTestAntiPatternsBlock is appended to unit-test generator and test-step fixer system prompts.
// It lists concrete forbidden patterns in C#, Java, and JS/TS so models avoid vacuous, self-referential, or skip-only tests.
const GarbageTestAntiPatternsBlock = `

### Forbidden "garbage" tests (do not generate, and do not "fix" by adding these)

Final tests must exercise behavior: call the unit under test (or render UI), use in-repo mocks/fakes, and assert observable outcomes. Do **not** ship the patterns below.

**C# (xUnit / NUnit / MSTest)**
- **Self-smoke:** a test that only constructs the **test class itself** and asserts it is non-null (e.g. a method named like CanCreateTestClass / Smoke that does Assert.NotNull(new MyTests()) on the fixture type). That does not test production code.
- **Ctor-only vacuous test:** a single new of the type under test plus only Assert.NotNull / Assert.NotSame(null, …) with no further calls, mocks, or outcome checks (e.g. Foo_CanBeConstructed with only allocation).
- **Empty skipped shell:** [Fact(Skip = "...")] / [Theory(Skip = "...")] / [Ignore] with an **empty** method body and no Arrange-Act-Assert — not a replacement for a real test. Prefer fakes, test doubles, or a minimal runnable scenario that compiles in-repo.
- **Infrastructure classification exception:** When an evaluator-classified infrastructure/environment failure applies (missing DB, invalid connection string), **one** honest skip/conditional guard for **that** failing method may match repo patterns from **Similar tests** — still forbid unrelated hollow stubs elsewhere in the file.

**Java (JUnit 5 / 4, TestNG-style)**
- **Self-smoke:** @Test method that only asserts the test class instance is non-null (assertNotNull(new MyTestClass())) — same anti-pattern as C#.
- **Ctor-only:** @Test that only does new Foo(); assertNotNull(foo); with no interaction with collaborators or public API beyond construction.
- **Empty @Disabled / @Ignored body:** a no-op disabled method added only to silence failures.

**JavaScript / TypeScript (Jest / Vitest / Mocha / Jasmine)**
- **Self-smoke:** it/test that only expects the test module or test class wrapper to be defined without importing and invoking production exports.
- **Ctor-only:** a single new ProductionType() plus only expect(x).toBeDefined() / not.toBeNull() with no further assertions or mock setup.
- **Empty skip:** it.skip / test.skip / describe.skip with an empty body as the shipped "fix".

**E2E:** do not replace failures with blanket empty skipped blocks; keep or add runnable steps, or one documented skip with minimal real interaction.

**Do instead (all languages):** call public (or test-visible) APIs, configure mocks from manifests/context, assert return values, errors, state changes, or verify(mock).`
