import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import {
  isLikelyE2ESpecPath,
  detectE2EFramework,
  enrichFileE2ESpec,
} from "./enrichers-e2e";
import type { FileSymbolsEdges } from "./enrichers";

describe("isLikelyE2ESpecPath", () => {
  it("matches e2e folder and cy extension", () => {
    expect(isLikelyE2ESpecPath("e2e/login.spec.ts")).toBe(true);
    expect(isLikelyE2ESpecPath("src/e2e/login.spec.ts")).toBe(true);
    expect(isLikelyE2ESpecPath("cypress/e2e/app.cy.ts")).toBe(true);
    expect(isLikelyE2ESpecPath("tests/foo.e2e.ts")).toBe(true);
  });
  it("rejects plain unit spec at repo root", () => {
    expect(isLikelyE2ESpecPath("src/components/Button.spec.ts")).toBe(false);
  });
  it("matches Nest-style e2e-spec and cypress root", () => {
    expect(isLikelyE2ESpecPath("test/app.e2e-spec.ts")).toBe(true);
    expect(isLikelyE2ESpecPath("cypress/support/e2e.ts")).toBe(true);
  });
});

describe("enrichFileE2ESpec", () => {
  it("adds E2E_SPEC for Playwright file under e2e/", () => {
    const source = `
import { test, expect } from '@playwright/test';

test('login', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('submit-btn').click();
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("tmp.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileE2ESpec(sf, entry, "tests.e2e.login.spec", "tests/e2e/login.spec.ts");

    expect(detectE2EFramework(sf)).toBe("playwright");
    const spec = entry.symbols.find((s) => s.kind === "E2E_SPEC");
    expect(spec).toBeDefined();
    expect(spec!.fq_name).toBe("E2E_SPEC:tests/e2e/login.spec.ts");
    expect(spec!.signature).toMatchObject({ framework: "playwright" });
    expect(entry.edges.some((e) => e.callee_fq_name === spec!.fq_name)).toBe(true);

    const sel = entry.symbols.find((s) => s.kind === "TEST_SELECTOR");
    expect(sel).toBeDefined();
    expect(sel!.signature).toMatchObject({ value: "submit-btn" });
    expect(entry.edges.some((e) => e.edge_type === "USES_SELECTOR" && e.callee_fq_name === sel!.fq_name)).toBe(
      true,
    );
  });

  it("adds E2E_SPEC for Playwright experimental CT React", () => {
    const source = `
import { test, expect } from '@playwright/experimental-ct-react';

test('renders', async ({ mount }) => {
  await mount(<div />);
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("playwright/components/Button.spec.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileE2ESpec(sf, entry, "playwright.components.Button.spec", "playwright/components/Button.spec.tsx");
    expect(detectE2EFramework(sf)).toBe("playwright");
    const spec = entry.symbols.find((s) => s.kind === "E2E_SPEC");
    expect(spec).toBeDefined();
  });

  it("adds E2E_SPEC for Cypress reference types + cy.visit", () => {
    const source = `/// <reference types="cypress" />
describe('smoke', () => {
  it('loads', () => {
    cy.visit('/');
  });
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("cypress/e2e/smoke.cy.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileE2ESpec(sf, entry, "cypress.e2e.smoke", "cypress/e2e/smoke.cy.ts");
    expect(detectE2EFramework(sf)).toBe("cypress");
    expect(entry.symbols.some((s) => s.kind === "E2E_SPEC")).toBe(true);
  });
});
