/**
 * Phase 6 — AngularJS (1.x): angular.module().controller/service/factory/... chains.
 */

import { SyntaxKind } from "ts-morph";
import type { CallExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";

const REG_KIND: Record<string, string> = {
  controller: "ANGULARJS_CONTROLLER",
  service: "ANGULARJS_SERVICE",
  factory: "ANGULARJS_FACTORY",
  directive: "ANGULARJS_DIRECTIVE",
  component: "ANGULARJS_COMPONENT",
  filter: "ANGULARJS_FILTER",
  provider: "ANGULARJS_PROVIDER",
};

function parseAngularModuleChain(outer: CallExpression): { moduleName: string; chain: { method: string; call: CallExpression }[] } | undefined {
  const chain: { method: string; call: CallExpression }[] = [];
  let cur: CallExpression = outer;
  while (true) {
    const callee = cur.getExpression();
    if (callee.isKind(SyntaxKind.PropertyAccessExpression)) {
      const name = callee.getName();
      if (name === "module") {
        const left = callee.getExpression();
        if (left.getText() === "angular") {
          const args = cur.getArguments();
          const a0 = args[0];
          const moduleName = a0?.isKind(SyntaxKind.StringLiteral) ? a0.getLiteralValue() : "";
          if (!moduleName) return undefined;
          return { moduleName, chain };
        }
        return undefined;
      }
      chain.unshift({ method: name, call: cur });
      const inner = callee.getExpression();
      if (!inner.isKind(SyntaxKind.CallExpression)) return undefined;
      cur = inner;
      continue;
    }
    return undefined;
  }
}

function firstStringArg(call: CallExpression): string {
  const a0 = call.getArguments()[0];
  if (a0?.isKind(SyntaxKind.StringLiteral)) return a0.getLiteralValue();
  return "?";
}

/**
 * Register AngularJS module and registration symbols from fluent API.
 */
export function enrichFileAngularJsGraph(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  const seenModules = new Set<string>();

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    // Skip inner `angular.module(...)` that is only the LHS of `.controller()` / `.service()` / …
    if (node.getParent()?.isKind(SyntaxKind.PropertyAccessExpression)) return;
    const parsed = parseAngularModuleChain(node);
    if (!parsed) return;

    const modFq = `ANGULARJS_MODULE:${parsed.moduleName}`;
    if (!seenModules.has(parsed.moduleName)) {
      seenModules.add(parsed.moduleName);
      entry.symbols.push({
        kind: "ANGULARJS_MODULE",
        fq_name: modFq,
        start_line: node.getStartLineNumber(),
        end_line: node.getEndLineNumber(),
        signature: { name: parsed.moduleName },
      });
      entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: modFq, edge_type: "CONTAINS" });
    }

    for (const reg of parsed.chain) {
      const kind = REG_KIND[reg.method];
      if (!kind) continue;
      const regName = firstStringArg(reg.call);
      const itemFq = `${kind}:${parsed.moduleName}:${regName}`;
      entry.symbols.push({
        kind,
        fq_name: itemFq,
        start_line: reg.call.getStartLineNumber(),
        end_line: reg.call.getEndLineNumber(),
        signature: { module: parsed.moduleName, registration: regName, method: reg.method },
      });
      entry.edges.push({ caller_fq_name: modFq, callee_fq_name: itemFq, edge_type: "CONTAINS" });
    }
  });
}
