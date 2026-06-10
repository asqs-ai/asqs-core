/**
 * Framework enrichers: add component graph and route graph symbols/edges.
 * Uses ts-morph SourceFile; single AST pass per file.
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import { fqName } from "./normalize";

/** Symbol/edge arrays for one file (mutated in place). */
export interface FileSymbolsEdges {
  symbols: {
    kind: string;
    fq_name: string;
    start_line: number;
    end_line: number;
    signature?: unknown;
  }[];
  edges: { caller_fq_name: string; callee_fq_name: string; edge_type: string }[];
}

/** Normalize HTTP path for stable API_ROUTE keys (leading slash, collapse slashes). */
export function normalizeHttpPath(prefix: string, pathArg: string): string {
  const a = (prefix || "").replace(/\/+/g, "/");
  const b = (pathArg || "").replace(/\/+/g, "/");
  let combined = a ? (b ? `${a}/${b}` : a) : b || "/";
  if (!combined.startsWith("/")) combined = "/" + combined;
  return combined.replace(/\/+/g, "/");
}

/**
 * Stable fq_name for a Nest HTTP route so metadata edges resolve to symbol IDs.
 * Uniqueness: same METHOD+path could theoretically repeat; methodFq disambiguates.
 */
export function nestApiRouteFQName(httpMethod: string, fullPath: string, handlerMethodFq: string): string {
  const m = httpMethod.toUpperCase();
  const p = normalizeHttpPath("", fullPath);
  return `API_ROUTE:${m}:${p}@${handlerMethodFq}`;
}

export { enrichFileReact } from "./enrichers-react-graph";

/**
 * Add Nest route graph for one file: NEST_CONTROLLER, NEST_ROUTE_HANDLER, API_ROUTE,
 * ROUTE_TO_HANDLER (API_ROUTE symbol → handler), CONTAINS (controller → API_ROUTE).
 *
 * API_ROUTE symbols exist so Go metadata edge insertion can resolve caller_fq_name to a symbol ID
 * (see internal/intelligence/indexer/run.go).
 */
export function enrichFileNest(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
): void {
  sf.forEachDescendant((node) => {
    if (node.isKind(SyntaxKind.ClassDeclaration)) {
      const decorators = node.getDecorators();
      let isController = false;
      let controllerPath = "";
      for (const d of decorators) {
        const call = d.getExpression();
        if (call.getKind?.() === SyntaxKind.CallExpression) {
          const expr = (call as { getExpression?: () => { getText?: () => string } }).getExpression?.();
          const name = expr?.getText?.() ?? "";
          if (name === "Controller") {
            isController = true;
            const args = (call as { getArguments?: () => unknown[] }).getArguments?.() ?? [];
            if (args.length > 0) {
              const first = args[0] as { getText?: () => string };
              controllerPath = first.getText?.()?.replace(/['"]/g, "") ?? "";
            }
            break;
          }
        }
      }
      if (!isController) return;

      const name = node.getName();
      if (!name) return;
      const classFq = fqName(moduleId, name);
      entry.symbols.push({
        kind: "NEST_CONTROLLER",
        fq_name: classFq,
        start_line: node.getStartLineNumber(),
        end_line: node.getEndLineNumber(),
      });

      for (const member of node.getMembers()) {
        if (!member.isKind(SyntaxKind.MethodDeclaration)) continue;
        const methodName = (member as { getName?: () => string }).getName?.();
        if (typeof methodName !== "string") continue;
        const methodFq = `${classFq}.${methodName}`;
        const memberDecorators = member.getDecorators();
        for (const d of memberDecorators) {
          const call = d.getExpression();
          if (call.getKind?.() === SyntaxKind.CallExpression) {
            const expr = (call as { getExpression?: () => { getText?: () => string } }).getExpression?.();
            const decName = expr?.getText?.() ?? "";
            const method = decName.toUpperCase();
            if (["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].includes(method)) {
              const args = (call as { getArguments?: () => unknown[] }).getArguments?.() ?? [];
              const pathArg = args.length > 0 ? (args[0] as { getText?: () => string }).getText?.()?.replace(/['"]/g, "") ?? "" : "";
              const fullPath = normalizeHttpPath(controllerPath, pathArg);
              const apiRouteFq = nestApiRouteFQName(method, fullPath, methodFq);

              entry.symbols.push({
                kind: "NEST_ROUTE_HANDLER",
                fq_name: methodFq,
                start_line: member.getStartLineNumber(),
                end_line: member.getEndLineNumber(),
              });
              entry.symbols.push({
                kind: "API_ROUTE",
                fq_name: apiRouteFq,
                start_line: member.getStartLineNumber(),
                end_line: member.getEndLineNumber(),
                signature: {
                  framework: "nest",
                  http_method: method,
                  path_pattern: fullPath,
                  handler_fq: methodFq,
                  controller_class: classFq,
                },
              });

              entry.edges.push({
                caller_fq_name: classFq,
                callee_fq_name: apiRouteFq,
                edge_type: "CONTAINS",
              });
              entry.edges.push({
                caller_fq_name: apiRouteFq,
                callee_fq_name: methodFq,
                edge_type: "ROUTE_TO_HANDLER",
              });
            }
          }
        }
      }
    }
  });
}
