Version: unit-v1

Goal:
- Generate high-signal unit tests that increase branch/behavior coverage, not duplicate existing happy paths.

Quality checklist:
- Exercise behavior via public API or narrow seam; avoid implementation-string assertions.
- Assert outcomes and side effects; verify collaborator interactions where meaningful.
- Prioritize uncovered branch intents: false paths, error/exception paths, boundary/null handling.
- Keep tests deterministic: no sleeps/time races/flaky network dependence.
- Minimize setup noise; prefer expressive builders/fixtures over opaque object dumps.

Language contracts:
- C#: xUnit/NUnit/MSTest patterns consistent with repo; prefer meaningful Assert + mock verification; avoid reflection-only tests.
- Java: JUnit 5 + Mockito (unless repo conventions differ); assert behavior and exception paths; avoid source-file text assertions.
- TS/JS: match detected unit runner (Jest/Vitest/Mocha/Jasmine); keep tests in dedicated test modules; avoid tautologies.

Anti-patterns (forbidden):
- Self-smoke tests (e.g., asserting test class construction only).
- Empty/skipped placeholder tests without concrete unblock reason.
- Tautological assertions (`expect(true).toBe(true)`, equal literal to same literal as sole check).
- Reasserting already covered scenarios when missing branch intents are provided.

Output contract:
- For new test files: emit a complete compilable test module/class.
- For extending existing tests: add focused cases for missing branch intents only; preserve existing style/naming.
