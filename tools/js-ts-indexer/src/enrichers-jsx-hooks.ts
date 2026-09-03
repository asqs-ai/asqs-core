/**
 * JSX test hooks: data-testid / data-cy / data-test / aria-label attributes on JSX elements.
 *
 * The HTML enricher (enrichers-html-hooks.ts) gives the E2E generator the selectors an Angular or
 * server-rendered page actually has. A React, Solid or Preact page has no template file — its
 * markup lives in .tsx/.jsx — so the inventory came back empty for every React repository
 * (`UI selector inventory: 0 selector(s) across 0 template(s)` in asqs-go run
 * api-9f854a955e0110668e02fec8d45198a5), and the generator guessed roles and titles: a
 * `getByRole('link', { name: 'Orders' })` that matched two links, and a page title that does not
 * exist. This enricher emits the same UI_TEST_HOOK symbols for JSX, so the Go inventory can list
 * them beside the HTML ones.
 *
 * Lifecycle is derived from the AST rather than from brace counting: an element inside a
 * conditional expression, a `&&` / `||` / `??` operand or an `if` branch is CONDITIONAL; one
 * inside a `.map(` / `.flatMap(` callback is REPEATED. Only literal attribute values are
 * indexed — `data-testid={`row-${id}`}` cannot be listed, and the inventory says so.
 */

import { SyntaxKind } from "ts-morph";
import type { JsxAttribute, Node, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";

/** Attribute names worth listing, with the selector_kind the Go inventory renders. */
const JSX_HOOK_ATTRIBUTES: Record<string, string> = {
  "data-testid": "data-testid",
  "data-test": "data-test",
  "data-cy": "data-cy",
  "aria-label": "aria-label",
};

function uiHookFqToken(s: string): string {
  return s.replace(/[@:/\\\s]/g, "_").slice(0, 64);
}

/** Literal string value of a JSX attribute, or null when it is dynamic or absent. */
function literalAttributeValue(attr: JsxAttribute): string | null {
  const init = attr.getInitializer();
  if (!init) return null;
  if (init.isKind(SyntaxKind.StringLiteral)) return init.getLiteralText();
  if (init.isKind(SyntaxKind.JsxExpression)) {
    const expr = init.getExpression();
    if (!expr) return null;
    if (expr.isKind(SyntaxKind.StringLiteral) || expr.isKind(SyntaxKind.NoSubstitutionTemplateLiteral)) {
      return expr.getLiteralText();
    }
  }
  return null;
}

/** Whether the JSX element carrying `node` renders only under a condition, or per item. */
function jsxLifecycle(node: Node): { conditional: boolean; repeated: boolean } {
  let conditional = false;
  let repeated = false;
  let cur: Node | undefined = node.getParent();
  while (cur) {
    // Stop at the enclosing function body's owner: a component's own `if` around its return is
    // still a condition, so keep walking through functions, but not past the file.
    if (cur.isKind(SyntaxKind.SourceFile)) break;
    if (cur.isKind(SyntaxKind.ConditionalExpression) || cur.isKind(SyntaxKind.IfStatement)) {
      conditional = true;
    } else if (cur.isKind(SyntaxKind.BinaryExpression)) {
      const op = cur.getOperatorToken().getKind();
      if (
        op === SyntaxKind.AmpersandAmpersandToken ||
        op === SyntaxKind.BarBarToken ||
        op === SyntaxKind.QuestionQuestionToken
      ) {
        conditional = true;
      }
    } else if (cur.isKind(SyntaxKind.CallExpression)) {
      const callee = cur.getExpression();
      if (callee.isKind(SyntaxKind.PropertyAccessExpression)) {
        const name = callee.getName();
        if (name === "map" || name === "flatMap") repeated = true;
      }
    }
    cur = cur.getParent();
  }
  return { conditional, repeated };
}

/**
 * Add UI_TEST_HOOK symbols for the literal test-hook attributes in one .tsx/.jsx file. Symbols are
 * appended to the file's own entry (lang typescript/javascript); `template_path` names the file
 * so the Go inventory groups them the way it groups HTML templates.
 */
export function enrichFileJsxHooks(sf: SourceFile, entry: FileSymbolsEdges, relPath: string): void {
  const pathKey = relPath.split("\\").join("/");
  const seen = new Set<string>();
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.JsxAttribute)) return;
    const attr = node as JsxAttribute;
    const name = attr.getNameNode().getText();
    const selectorKind = JSX_HOOK_ATTRIBUTES[name];
    if (!selectorKind) return;
    const value = literalAttributeValue(attr);
    if (!value || !value.trim()) return;
    const val = value.trim();
    const lineNum = attr.getStartLineNumber();
    const hookFq = `UI_TEST_HOOK:${selectorKind}:${uiHookFqToken(val)}@${pathKey}:L${lineNum}`;
    if (seen.has(hookFq)) return;
    seen.add(hookFq);
    const state = jsxLifecycle(attr);
    entry.symbols.push({
      kind: "UI_TEST_HOOK",
      fq_name: hookFq,
      start_line: lineNum,
      end_line: lineNum,
      signature: {
        selector_kind: selectorKind,
        value: val,
        framework: "jsx",
        template_path: pathKey,
        conditional: state.conditional,
        repeated: state.repeated,
      },
    });
  });
}
