/**
 * Vue Router 3/4: createRouter({ routes: [...] }), new VueRouter({ routes: [...] }).
 */

import { SyntaxKind } from "ts-morph";
import type { ArrayLiteralExpression, ObjectLiteralExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { resolveArrayLiteralArg, walkRouteConfigArray } from "./enrichers-page-route-common";

function fileMentionsVueRouter(sf: SourceFile): boolean {
  const t = sf.getFullText();
  return (
    /from\s+['"]vue-router['"]/.test(t) ||
    /require\s*\(\s*['"]vue-router['"]\s*\)/.test(t) ||
    /from\s+['"]@ionic\/vue-router['"]/.test(t)
  );
}

export function enrichFileVueRouter(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  if (!fileMentionsVueRouter(sf)) return;

  const seenArrays = new WeakSet<ArrayLiteralExpression>();
  const walkOnce = (arr: ArrayLiteralExpression): void => {
    if (seenArrays.has(arr)) return;
    seenArrays.add(arr);
    walkRouteConfigArray(arr, entry, moduleFq, "", "vue_router");
  };

  sf.forEachDescendant((node) => {
    if (node.isKind(SyntaxKind.CallExpression)) {
      const name = node.getExpression().getText().replace(/\s/g, "");
      if (name === "createRouter" || name.endsWith(".createRouter")) {
        const args = node.getArguments();
        if (args.length === 0) return;
        const opts = args[0];
        if (!opts.isKind(SyntaxKind.ObjectLiteralExpression)) return;
        extractRoutesFromOptionsObject(opts, sf, walkOnce);
      }
      return;
    }

    if (node.isKind(SyntaxKind.NewExpression)) {
      const target = node.getExpression().getText().replace(/\s/g, "");
      if (target !== "VueRouter") return;
      const args = node.getArguments();
      if (args.length === 0) return;
      const opts = args[0];
      if (!opts.isKind(SyntaxKind.ObjectLiteralExpression)) return;
      extractRoutesFromOptionsObject(opts, sf, walkOnce);
    }
  });

  for (const decl of sf.getVariableDeclarations()) {
    if (!/^(routes|routerRoutes)$/i.test(decl.getName())) continue;
    const init = decl.getInitializer();
    if (!init?.isKind(SyntaxKind.ArrayLiteralExpression)) continue;
    walkOnce(init.asKindOrThrow(SyntaxKind.ArrayLiteralExpression));
  }
}

function extractRoutesFromOptionsObject(
  obj: ObjectLiteralExpression,
  sf: SourceFile,
  walkOnce: (arr: ArrayLiteralExpression) => void,
): void {
  for (const prop of obj.getProperties()) {
    if (!prop.isKind(SyntaxKind.PropertyAssignment)) continue;
    if (prop.getName() !== "routes") continue;
    const init = prop.getInitializer();
    if (init?.isKind(SyntaxKind.ArrayLiteralExpression)) {
      walkOnce(init.asKindOrThrow(SyntaxKind.ArrayLiteralExpression));
      return;
    }
    const resolved = init ? resolveArrayLiteralArg(init, sf) : undefined;
    if (resolved) walkOnce(resolved);
  }
}
