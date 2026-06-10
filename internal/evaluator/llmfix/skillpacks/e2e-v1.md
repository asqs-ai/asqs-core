Version: e2e-v1

Fix strategy:
- Preserve E2E scenario intent; fix selectors/waits/isolation with minimal behavior drift.
- Keep stack-specific APIs consistent (Playwright vs Cypress vs language E2E stack).
- Stabilize flaky timing with explicit framework-native waiting patterns.

Forbidden fixes:
- Converting E2E tests into unit-style mocked tests.
- Blanket skipping/disabling instead of repairing a clear failure.
- Replacing assertions with non-behavioral/no-op checks.
