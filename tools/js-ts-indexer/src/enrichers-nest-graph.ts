/**
 * Phase 4 — NestJS module graph: @Module(), @Injectable(), constructor injection (static).
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import { fqName } from "./normalize";
import type { FileSymbolsEdges } from "./enrichers";

function isDecoratorNamed(node: { getExpression: () => unknown }, name: string): boolean {
  const expr = node.getExpression() as { getKind?: () => number; getExpression?: () => { getText: () => string }; getText?: () => string };
  if (expr.getKind?.() === SyntaxKind.CallExpression) {
    const inner = expr.getExpression?.();
    return inner?.getText() === name;
  }
  return expr.getText?.() === name;
}

function arrayIdentifierTexts(initializer: { getKind: () => number; getElements?: () => unknown[] }): string[] {
  if (initializer.getKind() !== SyntaxKind.ArrayLiteralExpression) return [];
  const els = initializer.getElements?.() ?? [];
  const out: string[] = [];
  for (const el of els) {
    const n = el as { getKind: () => number; getText: () => string };
    if (n.getKind() === SyntaxKind.Identifier) {
      out.push(n.getText());
    }
  }
  return out;
}

function parseModuleConfigObject(arg: {
  getKind: () => number;
  getProperties?: () => unknown[];
}): {
  imports: string[];
  controllers: string[];
  providers: string[];
  exports: string[];
} {
  const empty = { imports: [] as string[], controllers: [] as string[], providers: [] as string[], exports: [] as string[] };
  if (arg.getKind() !== SyntaxKind.ObjectLiteralExpression) return empty;
  const props = arg.getProperties?.() ?? [];
  const out = { ...empty };
  for (const p of props) {
    const prop = p as {
      getKind: () => number;
      getName?: () => string;
      getInitializer?: () => { getKind: () => number; getElements?: () => unknown[] };
    };
    if (prop.getKind() !== SyntaxKind.PropertyAssignment) continue;
    const key = prop.getName?.();
    if (!key || !["imports", "controllers", "providers", "exports"].includes(key)) continue;
    const init = prop.getInitializer?.();
    if (!init) continue;
    const ids = arrayIdentifierTexts(init);
    if (key === "imports") out.imports.push(...ids);
    if (key === "controllers") out.controllers.push(...ids);
    if (key === "providers") out.providers.push(...ids);
    if (key === "exports") out.exports.push(...ids);
  }
  return out;
}

function classHasInjectableDecorator(classDecl: {
  getDecorators: () => { getExpression: () => unknown }[];
}): boolean {
  for (const d of classDecl.getDecorators()) {
    const call = d.getExpression() as { getKind?: () => number; getExpression?: () => { getText: () => string }; getText?: () => string };
    if (call.getKind?.() === SyntaxKind.CallExpression && isDecoratorNamed(call as never, "Injectable")) {
      return true;
    }
    if (call.getKind?.() === SyntaxKind.Identifier && call.getText?.() === "Injectable") {
      return true;
    }
  }
  return false;
}

function classHasModuleDecorator(classDecl: {
  getDecorators: () => { getExpression: () => unknown }[];
}): boolean {
  for (const d of classDecl.getDecorators()) {
    const call = d.getExpression() as { getKind?: () => number; getExpression?: () => { getText: () => string }; getText?: () => string };
    if (call.getKind?.() === SyntaxKind.CallExpression && isDecoratorNamed(call as never, "Module")) {
      return true;
    }
    if (call.getKind?.() === SyntaxKind.Identifier && call.getText?.() === "Module") {
      return true;
    }
  }
  return false;
}

function constructorInjectionTypes(classDecl: {
  getMembers: () => unknown[];
}): string[] {
  const types: string[] = [];
  for (const mem of classDecl.getMembers()) {
    const m = mem as { getKind: () => number; getParameters?: () => unknown[] };
    if (m.getKind() !== SyntaxKind.Constructor) continue;
    const params = m.getParameters?.() ?? [];
    for (const param of params) {
      const p = param as { getTypeNode?: () => { getText: () => string } | undefined; getDecorators?: () => unknown[] };
      const tnode = p.getTypeNode?.();
      if (!tnode) continue;
      const text = tnode.getText().trim();
      if (!text || /^(string|number|boolean|bigint|symbol|any|unknown|void|never|null|undefined)$/i.test(text)) {
        continue;
      }
      types.push(text);
    }
  }
  return types;
}

/**
 * Nest @Module / @Injectable / constructor DI edges (does not replace HTTP route enricher).
 */
export function enrichNestModuleGraph(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.ClassDeclaration)) return;
    const name = node.getName();
    if (!name) return;
    const classFq = fqName(moduleId, name);
    const start = node.getStartLineNumber();
    const end = node.getEndLineNumber();

    if (classHasModuleDecorator(node)) {
      const nestModFq = `NEST_MODULE:${classFq}`;
      let cfg = { imports: [] as string[], controllers: [] as string[], providers: [] as string[], exports: [] as string[] };
      for (const d of node.getDecorators()) {
        const call = d.getExpression();
        if (call.getKind?.() !== SyntaxKind.CallExpression) continue;
        if (!isDecoratorNamed(call as never, "Module")) continue;
        const args = (call as { getArguments?: () => unknown[] }).getArguments?.() ?? [];
        if (args[0] && typeof (args[0] as { getKind?: () => number }).getKind === "function") {
          cfg = parseModuleConfigObject(args[0] as never);
        }
        break;
      }

      entry.symbols.push({
        kind: "NEST_MODULE",
        fq_name: nestModFq,
        start_line: start,
        end_line: end,
        signature: {
          class_fq: classFq,
          imports: cfg.imports,
          controllers: cfg.controllers,
          providers: cfg.providers,
          exports: cfg.exports,
        },
      });
      entry.edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: nestModFq,
        edge_type: "CONTAINS",
      });
      entry.edges.push({
        caller_fq_name: nestModFq,
        callee_fq_name: classFq,
        edge_type: "DECLARES",
      });

      for (const m of cfg.imports) {
        entry.edges.push({
          caller_fq_name: nestModFq,
          callee_fq_name: m,
          edge_type: "MODULE_IMPORTS",
        });
      }
      for (const c of cfg.controllers) {
        entry.edges.push({
          caller_fq_name: nestModFq,
          callee_fq_name: c,
          edge_type: "MODULE_REGISTERS",
        });
      }
      for (const p of cfg.providers) {
        entry.edges.push({
          caller_fq_name: nestModFq,
          callee_fq_name: p,
          edge_type: "MODULE_PROVIDES",
        });
      }
      for (const x of cfg.exports) {
        entry.edges.push({
          caller_fq_name: nestModFq,
          callee_fq_name: x,
          edge_type: "MODULE_EXPORTS",
        });
      }
    }

    if (classHasInjectableDecorator(node)) {
      const provFq = `NEST_PROVIDER:${classFq}`;
      entry.symbols.push({
        kind: "NEST_PROVIDER",
        fq_name: provFq,
        start_line: start,
        end_line: end,
        signature: { class_fq: classFq, role: "injectable" },
      });
      entry.edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: provFq,
        edge_type: "CONTAINS",
      });
      entry.edges.push({
        caller_fq_name: provFq,
        callee_fq_name: classFq,
        edge_type: "DECLARES",
      });

      for (const t of constructorInjectionTypes(node)) {
        entry.edges.push({
          caller_fq_name: classFq,
          callee_fq_name: t,
          edge_type: "INJECTS",
        });
      }
    }
  });
}
