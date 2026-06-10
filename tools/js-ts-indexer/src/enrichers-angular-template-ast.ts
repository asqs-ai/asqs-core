/**
 * Angular P2 — lightweight HTML template scan: structural directives, bindings, pipes (regex; not a full parser).
 */

import * as fs from "fs";
import * as path from "path";
import { SyntaxKind } from "ts-morph";
import type { ObjectLiteralExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";

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

function scanTemplateHtml(html: string): {
  structural: string[];
  events: string[];
  properties: string[];
  pipes: string[];
} {
  const structural: string[] = [];
  const events: string[] = [];
  const properties: string[] = [];
  const pipes: string[] = [];

  const strRe = /\*\s*(ngIf|ngFor|ngSwitch|ngTemplateOutlet)\b/g;
  let m: RegExpExecArray | null;
  while ((m = strRe.exec(html)) !== null) {
    structural.push(m[1]);
  }

  const evRe = /\(\s*([a-zA-Z][\w.-]*)\s*\)\s*=/g;
  while ((m = evRe.exec(html)) !== null) {
    events.push(m[1]);
  }

  const propRe = /\[\s*([^\]]+?)\s*\]/g;
  while ((m = propRe.exec(html)) !== null) {
    const raw = m[1].trim();
    if (raw.startsWith("(")) continue;
    properties.push(raw.split(/\s+/)[0]);
  }

  const pipeRe = /\{\{\s*[^}|]+\|\s*(\w+)/g;
  while ((m = pipeRe.exec(html)) !== null) {
    pipes.push(m[1]);
  }

  return {
    structural: [...new Set(structural)],
    events: [...new Set(events)],
    properties: [...new Set(properties)],
    pipes: [...new Set(pipes)],
  };
}

/**
 * For each `@Component({ templateUrl })`, read the HTML file and emit `ANGULAR_TEMPLATE_BINDING` symbols + edges.
 */
export function enrichAngularTemplateAst(sf: SourceFile, entry: FileSymbolsEdges, _moduleFq: string): void {
  const dir = path.dirname(sf.getFilePath());

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.ClassDeclaration)) return;
    for (const d of node.getDecorators()) {
      const ex = d.getExpression();
      if (!ex.isKind(SyntaxKind.CallExpression)) continue;
      const callee = ex.getExpression();
      if (!callee.isKind(SyntaxKind.Identifier) || callee.getText() !== "Component") continue;
      const a0 = ex.getArguments()[0];
      if (!a0?.isKind(SyntaxKind.ObjectLiteralExpression)) continue;
      const tu = stringProp(a0, "templateUrl");
      if (!tu) continue;

      const abs = path.resolve(dir, tu);
      if (!fs.existsSync(abs)) continue;

      let html: string;
      try {
        html = fs.readFileSync(abs, "utf-8");
      } catch {
        continue;
      }

      const tuPosix = tu.split(/[/\\]/).join("/");
      const scan = scanTemplateHtml(html);
      const tplSym = `ANGULAR_TEMPLATE:${tuPosix}`;

      let bindLine = 1;
      const pushBinding = (kind: string, name: string) => {
        const symFq = `ANGULAR_TEMPLATE_BINDING:${kind}:${name}@${tuPosix}:L${bindLine}`;
        bindLine += 1;
        entry.symbols.push({
          kind: "ANGULAR_TEMPLATE_BINDING",
          fq_name: symFq,
          start_line: 1,
          end_line: 1,
          signature: { template: tuPosix, binding_kind: kind, name },
        });
        entry.edges.push({
          caller_fq_name: tplSym,
          callee_fq_name: symFq,
          edge_type: "CONTAINS",
        });
      };

      for (const x of scan.structural) pushBinding("structural", x);
      for (const x of scan.events) pushBinding("event", x);
      for (const x of scan.properties) pushBinding("property", x);
      for (const x of scan.pipes) pushBinding("pipe", x);
    }
  });
}
