/**
 * Phase 6 — Angular (2+): @Component, @NgModule, @Injectable, @Directive, @Pipe, templates, DI.
 */

import { SyntaxKind } from "ts-morph";
import type { CallExpression, ClassDeclaration, Node, ObjectLiteralExpression, SourceFile } from "ts-morph";
import { fqName } from "./normalize";
import type { FileSymbolsEdges } from "./enrichers";

function decoratorInvokedName(decExpr: Node): string | undefined {
  if (decExpr.isKind(SyntaxKind.CallExpression)) {
    const c = decExpr.getExpression();
    if (c.isKind(SyntaxKind.Identifier)) return c.getText();
    if (c.isKind(SyntaxKind.PropertyAccessExpression)) return c.getName();
  }
  if (decExpr.isKind(SyntaxKind.Identifier)) return decExpr.getText();
  return undefined;
}

function stringProp(obj: ObjectLiteralExpression, key: string): string | undefined {
  for (const p of obj.getProperties()) {
    if (!p.isKind(SyntaxKind.PropertyAssignment)) continue;
    if (p.getName() !== key) continue;
    const init = p.getInitializer();
    if (!init) continue;
    if (init.isKind(SyntaxKind.StringLiteral)) return init.getLiteralValue();
    if (init.isKind(SyntaxKind.NoSubstitutionTemplateLiteral)) return init.getLiteralValue();
  }
  return undefined;
}

function booleanProp(obj: ObjectLiteralExpression, key: string): boolean | undefined {
  for (const p of obj.getProperties()) {
    if (!p.isKind(SyntaxKind.PropertyAssignment)) continue;
    if (p.getName() !== key) continue;
    const init = p.getInitializer();
    if (init?.isKind(SyntaxKind.TrueKeyword)) return true;
    if (init?.isKind(SyntaxKind.FalseKeyword)) return false;
  }
  return undefined;
}

function stringArrayProp(obj: ObjectLiteralExpression, key: string): string[] {
  const out: string[] = [];
  for (const p of obj.getProperties()) {
    if (!p.isKind(SyntaxKind.PropertyAssignment)) continue;
    if (p.getName() !== key) continue;
    const init = p.getInitializer();
    if (!init?.isKind(SyntaxKind.ArrayLiteralExpression)) continue;
    for (const el of init.getElements()) {
      if (el.isKind(SyntaxKind.StringLiteral)) out.push(el.getLiteralValue());
    }
  }
  return out;
}

function identifierArrayProp(obj: ObjectLiteralExpression, key: string): string[] {
  const out: string[] = [];
  for (const p of obj.getProperties()) {
    if (!p.isKind(SyntaxKind.PropertyAssignment)) continue;
    if (p.getName() !== key) continue;
    const init = p.getInitializer();
    if (!init?.isKind(SyntaxKind.ArrayLiteralExpression)) continue;
    for (const el of init.getElements()) {
      if (el.isKind(SyntaxKind.Identifier)) out.push(el.getText());
    }
  }
  return out;
}

function parseComponentObject(arg: ObjectLiteralExpression): Record<string, unknown> {
  const templateUrl = stringProp(arg, "templateUrl");
  const templateInline = stringProp(arg, "template");
  const selector = stringProp(arg, "selector");
  const standalone = booleanProp(arg, "standalone");
  const styleUrls = stringArrayProp(arg, "styleUrls");
  const imports = identifierArrayProp(arg, "imports");
  const inputs = stringArrayProp(arg, "inputs");
  const outputs = stringArrayProp(arg, "outputs");
  return {
    templateUrl,
    template_inline: Boolean(templateInline),
    selector,
    standalone: standalone ?? false,
    styleUrls,
    imports,
    inputs,
    outputs,
  };
}

function parseNgModuleObject(arg: ObjectLiteralExpression): {
  imports: string[];
  declarations: string[];
  exports: string[];
  providers: string[];
  bootstrap: string[];
} {
  const keys = ["imports", "declarations", "exports", "providers", "bootstrap"] as const;
  const out = {
    imports: [] as string[],
    declarations: [] as string[],
    exports: [] as string[],
    providers: [] as string[],
    bootstrap: [] as string[],
  };
  for (const p of arg.getProperties()) {
    if (!p.isKind(SyntaxKind.PropertyAssignment)) continue;
    const k = p.getName() as (typeof keys)[number];
    if (!keys.includes(k)) continue;
    const init = p.getInitializer();
    if (!init?.isKind(SyntaxKind.ArrayLiteralExpression)) continue;
    const ids: string[] = [];
    for (const el of init.getElements()) {
      if (el.isKind(SyntaxKind.Identifier)) ids.push(el.getText());
    }
    out[k] = ids;
  }
  return out;
}

function constructorInjectionTypes(classDecl: ClassDeclaration): string[] {
  const types: string[] = [];
  for (const mem of classDecl.getMembers()) {
    if (!mem.isKind(SyntaxKind.Constructor)) continue;
    for (const param of mem.getParameters()) {
      const tnode = param.getTypeNode();
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

function collectInputOutputFromMembers(classDecl: ClassDeclaration): { inputs: string[]; outputs: string[] } {
  const inputs: string[] = [];
  const outputs: string[] = [];
  for (const mem of classDecl.getMembers()) {
    if (!mem.isKind(SyntaxKind.PropertyDeclaration)) continue;
    const propName = mem.getName();
    for (const d of mem.getDecorators()) {
      const n = decoratorInvokedName(d.getExpression());
      if (n === "Input") {
        const args = d.getExpression().isKind(SyntaxKind.CallExpression)
          ? (d.getExpression() as CallExpression).getArguments()
          : [];
        const alias =
          args[0]?.isKind(SyntaxKind.StringLiteral) ? args[0].getLiteralValue() : String(propName);
        inputs.push(alias);
      }
      if (n === "Output") {
        const args = d.getExpression().isKind(SyntaxKind.CallExpression)
          ? (d.getExpression() as CallExpression).getArguments()
          : [];
        const alias =
          args[0]?.isKind(SyntaxKind.StringLiteral) ? args[0].getLiteralValue() : String(propName);
        outputs.push(alias);
      }
    }
  }
  return { inputs, outputs };
}

/**
 * Angular decorators, NgModule graph, external templates, constructor DI.
 */
export function enrichFileAngularGraph(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.ClassDeclaration)) return;
    const className = node.getName();
    if (!className) return;
    const classFq = fqName(moduleId, className);

    let hasComponent = false;
    let hasDirective = false;
    let hasPipe = false;
    let hasNgModule = false;
    let hasInjectable = false;

    for (const d of node.getDecorators()) {
      const expr = d.getExpression();
      const decName = decoratorInvokedName(expr);
      if (!decName) continue;

      if (decName === "Component") {
        hasComponent = true;
        let meta: Record<string, unknown> = {};
        if (expr.isKind(SyntaxKind.CallExpression)) {
          const a0 = expr.getArguments()[0];
          if (a0?.isKind(SyntaxKind.ObjectLiteralExpression)) {
            meta = parseComponentObject(a0);
          }
        }
        const io = collectInputOutputFromMembers(node);
        if (io.inputs.length) meta.field_inputs = io.inputs;
        if (io.outputs.length) meta.field_outputs = io.outputs;

        const symFq = `ANGULAR_COMPONENT:${classFq}`;
        entry.symbols.push({
          kind: "ANGULAR_COMPONENT",
          fq_name: symFq,
          start_line: node.getStartLineNumber(),
          end_line: node.getEndLineNumber(),
          signature: { class_fq: classFq, ...meta },
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: symFq, edge_type: "CONTAINS" });
        entry.edges.push({ caller_fq_name: symFq, callee_fq_name: classFq, edge_type: "DECLARES" });

        const tu = meta.templateUrl as string | undefined;
        if (tu) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: tu, edge_type: "USES_TEMPLATE" });
          const tplFq = `ANGULAR_TEMPLATE:${tu}`;
          entry.symbols.push({
            kind: "ANGULAR_TEMPLATE",
            fq_name: tplFq,
            start_line: 1,
            end_line: 1,
            signature: { path: tu, component_sym: symFq },
          });
        }

        for (const imp of (meta.imports as string[] | undefined) ?? []) {
          entry.edges.push({
            caller_fq_name: symFq,
            callee_fq_name: imp,
            edge_type: "STANDALONE_IMPORTS",
          });
        }
        continue;
      }

      if (decName === "Directive") {
        hasDirective = true;
        let selector: string | undefined;
        if (expr.isKind(SyntaxKind.CallExpression)) {
          const a0 = expr.getArguments()[0];
          if (a0?.isKind(SyntaxKind.ObjectLiteralExpression)) {
            selector = stringProp(a0, "selector");
          }
        }
        const symFq = `ANGULAR_DIRECTIVE:${classFq}`;
        entry.symbols.push({
          kind: "ANGULAR_DIRECTIVE",
          fq_name: symFq,
          start_line: node.getStartLineNumber(),
          end_line: node.getEndLineNumber(),
          signature: { class_fq: classFq, selector },
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: symFq, edge_type: "CONTAINS" });
        entry.edges.push({ caller_fq_name: symFq, callee_fq_name: classFq, edge_type: "DECLARES" });
        continue;
      }

      if (decName === "Pipe") {
        hasPipe = true;
        let pipeName: string | undefined;
        if (expr.isKind(SyntaxKind.CallExpression)) {
          const a0 = expr.getArguments()[0];
          if (a0?.isKind(SyntaxKind.ObjectLiteralExpression)) {
            pipeName = stringProp(a0, "name");
          }
        }
        const symFq = `ANGULAR_PIPE:${classFq}`;
        entry.symbols.push({
          kind: "ANGULAR_PIPE",
          fq_name: symFq,
          start_line: node.getStartLineNumber(),
          end_line: node.getEndLineNumber(),
          signature: { class_fq: classFq, pipe_name: pipeName },
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: symFq, edge_type: "CONTAINS" });
        entry.edges.push({ caller_fq_name: symFq, callee_fq_name: classFq, edge_type: "DECLARES" });
        continue;
      }

      if (decName === "NgModule") {
        hasNgModule = true;
        let cfg = {
          imports: [] as string[],
          declarations: [] as string[],
          exports: [] as string[],
          providers: [] as string[],
          bootstrap: [] as string[],
        };
        if (expr.isKind(SyntaxKind.CallExpression)) {
          const a0 = expr.getArguments()[0];
          if (a0?.isKind(SyntaxKind.ObjectLiteralExpression)) {
            cfg = parseNgModuleObject(a0);
          }
        }
        const symFq = `ANGULAR_MODULE:${classFq}`;
        entry.symbols.push({
          kind: "ANGULAR_MODULE",
          fq_name: symFq,
          start_line: node.getStartLineNumber(),
          end_line: node.getEndLineNumber(),
          signature: { class_fq: classFq, ...cfg },
        });
        entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: symFq, edge_type: "CONTAINS" });
        entry.edges.push({ caller_fq_name: symFq, callee_fq_name: classFq, edge_type: "DECLARES" });

        for (const x of cfg.imports) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: x, edge_type: "MODULE_IMPORTS" });
        }
        for (const x of cfg.declarations) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: x, edge_type: "MODULE_DECLARATIONS" });
        }
        for (const x of cfg.exports) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: x, edge_type: "MODULE_EXPORTS" });
        }
        for (const x of cfg.providers) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: x, edge_type: "MODULE_PROVIDES" });
        }
        for (const x of cfg.bootstrap) {
          entry.edges.push({ caller_fq_name: symFq, callee_fq_name: x, edge_type: "MODULE_BOOTSTRAP" });
        }
        continue;
      }

      if (decName === "Injectable") {
        hasInjectable = true;
      }
    }

    if (hasInjectable && !hasComponent && !hasDirective && !hasPipe && !hasNgModule) {
      const symFq = `ANGULAR_SERVICE:${classFq}`;
      entry.symbols.push({
        kind: "ANGULAR_SERVICE",
        fq_name: symFq,
        start_line: node.getStartLineNumber(),
        end_line: node.getEndLineNumber(),
        signature: { class_fq: classFq },
      });
      entry.edges.push({ caller_fq_name: moduleFq, callee_fq_name: symFq, edge_type: "CONTAINS" });
      entry.edges.push({ caller_fq_name: symFq, callee_fq_name: classFq, edge_type: "DECLARES" });
    }

    if (hasInjectable || hasComponent || hasDirective || hasPipe) {
      for (const t of constructorInjectionTypes(node)) {
        entry.edges.push({ caller_fq_name: classFq, callee_fq_name: t, edge_type: "INJECTS" });
      }
    }
  });
}
