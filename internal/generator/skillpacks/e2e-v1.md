Version: e2e-v1

Goal:
- Generate reliable scenario-driven E2E/integration tests aligned to the configured stack and uncovered flow risk.

Quality checklist:
- Validate user/system behavior end-to-end through stable selectors/contracts.
- Query elements by user-visible properties in this priority: ARIA role (getByRole / findByRole), label or placeholder text, visible text content, then data-testid attribute; avoid selector chains tied to CSS class names, auto-generated IDs, or DOM nesting depth.
- Cover unhappy/error flows and authorization/validation branches when available.
- For error and authorization flows, intercept network requests to return specific server responses (page.route() in Playwright, cy.intercept() in Cypress); do not rely on timeouts or real backend error states to exercise these branches.
- Keep specs deterministic and isolated (fresh state, bounded retries, explicit waits only when justified).
- Seed pre-conditions via API requests or fixture injection (Playwright request fixture, cy.request() in Cypress) rather than UI navigation; reserve UI flows for the behavior under test.
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
- Explicit waitForTimeout(ms) / cy.wait(ms) as a substitute for waiting on a specific condition; use waitForSelector, waitForResponse, or the framework's built-in actionability checks instead.

Output contract:
- New spec path must match stack conventions.
- When extending existing specs, add only missing scenarios/branches and keep naming/reporting style consistent.
