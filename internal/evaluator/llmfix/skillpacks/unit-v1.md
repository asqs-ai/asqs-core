Version: unit-v1

Fix strategy:
- Preserve test intent and repair failing behavior checks; do not degrade coverage.
- Prefer branch-focused fixes (error/null/boundary/false-path) when existing tests already cover happy paths.
- Keep framework-native assertions/mocking and repository style consistency.

Forbidden fixes:
- Replacing behavior tests with tautologies or self-smoke checks.
- Empty or skipped tests as the primary fix.
- Source-text assertion tests (`readFile + toContain`) instead of real invocation assertions.
