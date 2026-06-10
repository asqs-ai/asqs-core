Version: e2e-v1

Goal:
- Generate reliable scenario-driven E2E/integration tests aligned to the configured stack and uncovered flow risk.

Quality checklist:
- Validate user/system behavior end-to-end through stable selectors/contracts.
- Cover unhappy/error flows and authorization/validation branches when available.
- Keep specs deterministic and isolated (fresh state, bounded retries, explicit waits only when justified).
- Use page-object/helper abstractions when repo patterns already exist.
- Prefer active tests; do not blanket-disable/skip due to generic CI assumptions.

Stack contracts:
- Playwright: use @playwright/test APIs and placement conventions from repo config.
- Cypress: use cy.* APIs and cypress/e2e spec conventions.
- Java/.NET E2E: use the configured browser/runner stack in project conventions (Playwright/Selenium/etc.).

Anti-patterns (forbidden):
- Mixing unit-test mocking styles (jest.mock/vi.mock) into browser E2E specs unless repo already combines them intentionally.
- Snapshot-only assertions without behavior validation.
- Flaky timing assertions tied to arbitrary sleeps.

Output contract:
- New spec path must match stack conventions.
- When extending existing specs, add only missing scenarios/branches and keep naming/reporting style consistent.
