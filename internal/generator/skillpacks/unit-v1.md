Version: unit-v1

Goal:
- Generate high-signal unit tests that increase branch/behavior coverage, not duplicate existing happy paths.

Quality checklist:
- Structure each test as a single Arrange-Act-Assert: one logical action per test method; split tests that drive more than one behavior.
- Name tests as specifications: encode what is tested, under what condition, and what is expected (e.g. <unit>_<scenario>_<expected> or "should <outcome> when <condition>"); a reader must understand the failure from the name alone.
- Exercise behavior via public API or narrow seam; avoid implementation-string assertions.
- Assert collaborator interactions only on commands (side effects): use stubs to supply inputs and do not assert on them; use mocks/spies to verify that a call with specific arguments was made when that call is the primary observable output.
- Prefer assertions that would fail if the return value, exception type, or side effect changed; coverage achieved by assertions that pass regardless of output is not valuable.
- Prioritize uncovered branch intents: false paths, error/exception paths, boundary/null handling.
- Keep tests deterministic: no sleeps/time races/flaky network dependence.
- Minimize setup noise; prefer expressive builders/fixtures over opaque object dumps.

Language contracts:
- C#: xUnit/NUnit/MSTest patterns consistent with repo; prefer meaningful Assert + mock verification; avoid reflection-only tests. Use [Theory] + [InlineData] / [MemberData] for data-driven boundary cases.
- Java: JUnit 5 + Mockito (unless repo conventions differ); assert behavior and exception paths; avoid source-file text assertions. Use @ParameterizedTest + @CsvSource / @MethodSource for boundary and equivalence-partition sets.
- TS/JS: match detected unit runner (Jest/Vitest/Mocha/Jasmine); keep tests in dedicated test modules; avoid tautologies. Use test.each / it.each for tabular boundary or equivalence-partition coverage.

Anti-patterns (forbidden):
- Self-smoke tests (e.g., asserting test class construction only).
- Empty/skipped placeholder tests without concrete unblock reason.
- Tautological assertions (`expect(true).toBe(true)`, equal literal to same literal as sole check).
- Reasserting already covered scenarios when missing branch intents are provided.

Output contract:
- For new test files: emit a complete compilable test module/class.
- For extending existing tests: add focused cases for missing branch intents only; preserve existing style/naming.
