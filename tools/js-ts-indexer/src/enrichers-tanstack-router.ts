/**
 * TanStack Router — `createFileRoute('/path')({ ... })` (curried call pattern).
 */

import { SyntaxKind } from "ts-morph";
import type { CallExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { pushPageRoute } from "./enrichers-page-route-common";

function stringLiteralText(node: import("ts-morph").Node): string | undefined {
  if (node.isKind(SyntaxKind.StringLiteral)) return node.getLiteralValue();
  if (node.isKind(SyntaxKind.NoSubstitutionTemplateLiteral)) return node.getLiteralValue();
  const t = node.getText().trim();
  const m = t.match(/^['"]([^'"]*)['"]$/);
  return m ? m[1] : undefined;
}

function pathFromCreateFileRouteCall(inner: CallExpression): string | undefined {
  const callee = inner.getExpression().getText().replace(/\s/g, "");
  if (callee !== "createFileRoute" && !callee.endsWith(".createFileRoute")) return undefined;
  const a0 = inner.getArguments()[0];
  if (!a0) return undefined;
  return stringLiteralText(a0);
}

/**
 * Detect `createFileRoute('/segment')({ component: … })` outer call.
 */
export function enrichTanStackFileRoutes(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const outerCallee = node.getExpression();
    if (!outerCallee.isKind(SyntaxKind.CallExpression)) return;
    const pathStr = pathFromCreateFileRouteCall(outerCallee);
    if (!pathStr) return;
    const start = node.getStartLineNumber();
    const end = node.getEndLineNumber();
    pushPageRoute(entry, moduleFq, pathStr, start, end, "tanstack_router");
  });
}
