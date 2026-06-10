/**
 * NestJS P2 — DTO-shaped parameters (@Body/@Query/…), @UseGuards, @UsePipes, @UseInterceptors on controllers.
 */

import { SyntaxKind } from "ts-morph";
import type { CallExpression, ClassDeclaration, MethodDeclaration, SourceFile } from "ts-morph";
import { fqName } from "./normalize";
import type { FileSymbolsEdges } from "./enrichers";

function decoratorCallName(expr: CallExpression): string | undefined {
  const e = expr.getExpression();
  if (e.isKind(SyntaxKind.Identifier)) return e.getText();
  if (e.isKind(SyntaxKind.PropertyAccessExpression)) return e.getName();
  return undefined;
}

/** Identifiers from decorator args: UseGuards(A, B) or UseGuards([A, B]). */
function identifiersFromDecoratorArgs(call: CallExpression): string[] {
  const out: string[] = [];
  for (const arg of call.getArguments()) {
    if (arg.isKind(SyntaxKind.Identifier)) {
      out.push(arg.getText());
      continue;
    }
    if (arg.isKind(SyntaxKind.ArrayLiteralExpression)) {
      for (const el of arg.getElements()) {
        if (el.isKind(SyntaxKind.Identifier)) out.push(el.getText());
      }
    }
  }
  return out;
}

function isNestParamDecorator(name: string): boolean {
  return ["Body", "Query", "Param", "Headers", "Session", "Req", "Res"].includes(name);
}

function hasNestParamDecorator(method: MethodDeclaration): boolean {
  for (const p of method.getParameters()) {
    for (const d of p.getDecorators()) {
      const ex = d.getExpression();
      if (!ex.isKind(SyntaxKind.CallExpression)) continue;
      const n = decoratorCallName(ex);
      if (n && isNestParamDecorator(n)) return true;
    }
  }
  return false;
}

/** Type text suitable as DTO reference (class/interface name, not primitive). */
function dtoTypeFromParameter(param: import("ts-morph").ParameterDeclaration): string | undefined {
  const tn = param.getTypeNode();
  if (!tn) return undefined;
  const text = tn.getText().trim();
  if (!text || text.length > 120) return undefined;
  if (/^(string|number|boolean|bigint|symbol|any|unknown|void|never|null|undefined|object)$/i.test(text)) {
    return undefined;
  }
  // Skip inline object types and unions for stable fq targets
  if (text.includes("{") || text.includes("|") || text.includes("&")) return undefined;
  return text;
}

function classLevelDecoratorTargets(
  cls: ClassDeclaration,
  names: Set<string>,
): Map<string, string[]> {
  const byName = new Map<string, string[]>();
  for (const d of cls.getDecorators()) {
    const ex = d.getExpression();
    if (!ex.isKind(SyntaxKind.CallExpression)) continue;
    const n = decoratorCallName(ex);
    if (!n || !names.has(n)) continue;
    byName.set(n, identifiersFromDecoratorArgs(ex));
  }
  return byName;
}

function methodDecoratorTargets(
  method: MethodDeclaration,
  names: Set<string>,
): Map<string, string[]> {
  const byName = new Map<string, string[]>();
  for (const d of method.getDecorators()) {
    const ex = d.getExpression();
    if (!ex.isKind(SyntaxKind.CallExpression)) continue;
    const n = decoratorCallName(ex);
    if (!n || !names.has(n)) continue;
    byName.set(n, identifiersFromDecoratorArgs(ex));
  }
  return byName;
}

function pushNestDecoratorSymbol(
  entry: FileSymbolsEdges,
  kind: "NEST_GUARD" | "NEST_PIPE" | "NEST_INTERCEPTOR",
  id: string,
  moduleId: string,
  line: number,
  endLine: number,
  extra: Record<string, string>,
): string {
  const symFq = `${kind}:${id}@${moduleId}:L${line}`;
  entry.symbols.push({
    kind,
    fq_name: symFq,
    start_line: line,
    end_line: endLine,
    signature: { identifier: id, ...extra },
  });
  return symFq;
}

/**
 * Controller-only: HANDLER_USES_DTO, USES_GUARD, USES_PIPE, USES_INTERCEPTOR edges + lightweight decorator symbols.
 */
export function enrichNestDtoGuardsPipes(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
): void {
  const decNames = new Set(["UseGuards", "UsePipes", "UseInterceptors"]);

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.ClassDeclaration)) return;
    const className = node.getName();
    if (!className) return;

    let isController = false;
    for (const d of node.getDecorators()) {
      const ex = d.getExpression();
      if (!ex.isKind(SyntaxKind.CallExpression)) continue;
      const inner = ex.getExpression();
      if (inner.getText() === "Controller") {
        isController = true;
        break;
      }
    }
    if (!isController) return;

    const classFq = fqName(moduleId, className);
    const classDecos = classLevelDecoratorTargets(node, decNames);

    for (const member of node.getMembers()) {
      if (!member.isKind(SyntaxKind.MethodDeclaration)) continue;
      const methodName = member.getName();
      if (typeof methodName !== "string") continue;
      const methodFq = `${classFq}.${methodName}`;
      const mStart = member.getStartLineNumber();
      const mEnd = member.getEndLineNumber();

      const methodDecos = methodDecoratorTargets(member, decNames);
      const mergeDeco = (n: string): string[] => {
        const m = methodDecos.get(n);
        if (m && m.length > 0) return m;
        return classDecos.get(n) ?? [];
      };

      for (const id of mergeDeco("UseGuards")) {
        const gFq = pushNestDecoratorSymbol(entry, "NEST_GUARD", id, moduleId, mStart, mEnd, {
          on: methodFq,
        });
        entry.edges.push({ caller_fq_name: methodFq, callee_fq_name: gFq, edge_type: "USES_GUARD" });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: gFq, edge_type: "CONTAINS" });
      }
      for (const id of mergeDeco("UsePipes")) {
        const pFq = pushNestDecoratorSymbol(entry, "NEST_PIPE", id, moduleId, mStart, mEnd, {
          on: methodFq,
        });
        entry.edges.push({ caller_fq_name: methodFq, callee_fq_name: pFq, edge_type: "USES_PIPE" });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: pFq, edge_type: "CONTAINS" });
      }
      for (const id of mergeDeco("UseInterceptors")) {
        const iFq = pushNestDecoratorSymbol(entry, "NEST_INTERCEPTOR", id, moduleId, mStart, mEnd, {
          on: methodFq,
        });
        entry.edges.push({
          caller_fq_name: methodFq,
          callee_fq_name: iFq,
          edge_type: "USES_INTERCEPTOR",
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: iFq, edge_type: "CONTAINS" });
      }

      // DTO: parameters with @Body/@Query/… and a non-primitive type
      if (!hasNestParamDecorator(member)) continue;
      for (const param of member.getParameters()) {
        let hit = false;
        for (const d of param.getDecorators()) {
          const ex = d.getExpression();
          if (!ex.isKind(SyntaxKind.CallExpression)) continue;
          const n = decoratorCallName(ex);
          if (n && isNestParamDecorator(n)) {
            hit = true;
            break;
          }
        }
        if (!hit) continue;
        const dtoText = dtoTypeFromParameter(param);
        if (!dtoText) continue;
        const dtoFq = `DTO:${dtoText}@${methodFq}`;
        entry.symbols.push({
          kind: "DTO",
          fq_name: dtoFq,
          start_line: param.getStartLineNumber(),
          end_line: param.getEndLineNumber(),
          signature: { type_text: dtoText, handler_fq: methodFq },
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: dtoFq, edge_type: "CONTAINS" });
        entry.edges.push({
          caller_fq_name: methodFq,
          callee_fq_name: dtoText,
          edge_type: "HANDLER_USES_DTO",
        });
      }
    }
  });
}
