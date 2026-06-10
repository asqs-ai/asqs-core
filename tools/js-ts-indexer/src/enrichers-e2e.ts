/**
 * E2E-oriented enrichers: E2E_SPEC symbols for Playwright/Cypress-style spec files.
 * See docs/E2E-INDEXING.md for symbol/edge conventions.
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";

/** Repo-relative path heuristics — avoids tagging ordinary Jest unit *.spec.ts files under src/ alone. */
export function isLikelyE2ESpecPath(relPath: string): boolean {
  const p = relPath.replace(/\\/g, "/").toLowerCase();
  // Align with Go indexer.IsLikelyTestSourcePath: repo-root e2e/ as well as .../e2e/...
  if (p.startsWith("e2e/") || p.includes("/e2e/")) return true;
  if (p.startsWith("cypress/") || p.includes("/cypress/")) return true;
  if (p.includes("playwright")) return true;
  if (/\.e2e\.(ts|tsx|js|jsx)$/i.test(p)) return true;
  if (/\.e2e-spec\.(ts|tsx|js|jsx)$/i.test(p)) return true;
  if (/\.cy\.(ts|tsx|js|jsx)$/i.test(p)) return true;
  if (p.includes("/nightwatch/") || p.includes("/wdio/") || p.includes("webdriverio")) return true;
  return false;
}

export type E2EFrameworkHint = "playwright" | "cypress" | "unknown";

function pathHintsPlaywrightOrCypress(filePath: string): { playwright: boolean; cypress: boolean } {
  const p = filePath.replace(/\\/g, "/").toLowerCase();
  return {
    playwright: p.includes("playwright"),
    cypress: p.startsWith("cypress/") || p.includes("/cypress/") || /\.cy\.[tj]sx?$/i.test(p),
  };
}

export function detectE2EFramework(sf: SourceFile): E2EFrameworkHint {
  const fp = sf.getFilePath();
  const pathHint = pathHintsPlaywrightOrCypress(fp);

  for (const imp of sf.getImportDeclarations()) {
    const spec = imp.getModuleSpecifierValue() || "";
    if (
      spec.includes("@playwright/test") ||
      spec.includes("@playwright/experimental-ct-react") ||
      spec.includes("@playwright/experimental-ct-vue") ||
      spec.includes("@playwright/experimental-ct-svelte") ||
      spec === "playwright/test" ||
      spec.startsWith("playwright/")
    ) {
      return "playwright";
    }
    if (spec === "cypress" || spec.startsWith("cypress/")) return "cypress";
  }
  const text = sf.getFullText();
  if (
    text.includes("@playwright/test") ||
    text.includes("@playwright/experimental-ct-react") ||
    text.includes("@playwright/experimental-ct-vue") ||
    text.includes("@playwright/experimental-ct-svelte") ||
    /from\s+["']playwright\/test["']/.test(text)
  ) {
    return "playwright";
  }
  if (/\/\/\/\s*<reference\s+types=["']cypress["']\s*\/>/.test(text)) return "cypress";
  if (/\bcypress\b/.test(text) && /\.(visit|get|contains|request|intercept)\(/.test(text)) return "cypress";
  // Cypress globals in spec files (no import)
  if (pathHint.cypress && /\bcy\./.test(text)) return "cypress";
  if (pathHint.playwright && /\b(expect|test|page)\s*[.(]/.test(text)) return "playwright";
  return "unknown";
}

/** First top-level `test(` / `it(` call (line range) for chunking; skips nested depth > 0 heuristically. */
export function findFirstTestCallRange(sf: SourceFile): { start: number; end: number } | undefined {
  let found: { start: number; end: number } | undefined;
  sf.forEachDescendant((node) => {
    if (found) return;
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const expr = node.getExpression();
    const text = expr.getText().replace(/\s/g, "");
    if (
      text === "test" ||
      text.endsWith(".test") ||
      text === "it" ||
      text.endsWith(".it") ||
      text === "describe" ||
      text.endsWith(".describe") ||
      text === "context" ||
      text.endsWith(".context") ||
      text === "suite" ||
      text.endsWith(".suite")
    ) {
      found = {
        start: node.getStartLineNumber(),
        end: node.getEndLineNumber(),
      };
    }
  });
  return found;
}

/**
 * Playwright: page.getByTestId('x') / getByTestId("x") → TEST_SELECTOR + USES_SELECTOR from E2E_SPEC.
 */
export function enrichPlaywrightSelectors(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
  e2eSpecFq: string,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const expr = node.getExpression();
    const text = expr.getText().replace(/\s/g, "");
    if (text !== "getByTestId" && !text.endsWith(".getByTestId")) return;
    const args = node.getArguments();
    if (args.length === 0) return;
    const a0 = args[0];
    if (!a0.isKind(SyntaxKind.StringLiteral)) return;
    const id = a0.getLiteralText();
    const line = node.getStartLineNumber();
    const selFq = `TEST_SELECTOR:testid:${id}@${moduleFq}:L${line}`;
    entry.symbols.push({
      kind: "TEST_SELECTOR",
      fq_name: selFq,
      start_line: line,
      end_line: node.getEndLineNumber(),
      signature: { selector_kind: "testid", value: id, framework: "playwright" },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: selFq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: e2eSpecFq,
      callee_fq_name: selFq,
      edge_type: "USES_SELECTOR",
    });
  });
}

/**
 * Adds E2E_SPEC symbol + MODULE CONTAINS E2E_SPEC when path and imports look like E2E tests.
 */
export function enrichFileE2ESpec(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
  relPath: string,
): void {
  if (!isLikelyE2ESpecPath(relPath)) return;
  const fw = detectE2EFramework(sf);
  if (fw === "unknown") return;
  const range = findFirstTestCallRange(sf);
  if (!range) return;

  const pathKey = relPath.split(/[/\\]/).join("/");
  const fq = `E2E_SPEC:${pathKey}`;

  entry.symbols.push({
    kind: "E2E_SPEC",
    fq_name: fq,
    start_line: range.start,
    end_line: range.end,
    signature: {
      framework: fw,
      spec_path: pathKey,
    },
  });
  entry.edges.push({
    caller_fq_name: moduleFq,
    callee_fq_name: fq,
    edge_type: "CONTAINS",
  });

  if (fw === "playwright") {
    enrichPlaywrightSelectors(sf, entry, moduleFq, fq);
  }
}
