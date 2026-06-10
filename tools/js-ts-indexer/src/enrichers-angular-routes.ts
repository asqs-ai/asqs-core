/**
 * Angular Router: RouterModule.forRoot/forChild, provideRouter([...]), and common `routes` arrays.
 */

import { SyntaxKind } from "ts-morph";
import type { ArrayLiteralExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { resolveArrayLiteralArg, walkRouteConfigArray } from "./enrichers-page-route-common";

const ROUTES_VAR = /^(routes|appRoutes|APP_ROUTES)$/i;

export function enrichFileAngularRoutes(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  const seenArrays = new WeakSet<ArrayLiteralExpression>();

  const walkOnce = (arr: ArrayLiteralExpression): void => {
    if (seenArrays.has(arr)) return;
    seenArrays.add(arr);
    walkRouteConfigArray(arr, entry, moduleFq, "", "angular_router");
  };

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const name = node.getExpression().getText().replace(/\s/g, "");

    if (name === "provideRouter" || name.endsWith(".provideRouter")) {
      const args = node.getArguments();
      if (args.length === 0) return;
      const arr = resolveArrayLiteralArg(args[0], sf);
      if (arr) walkOnce(arr);
      return;
    }

    if (
      (name.includes("RouterModule") && name.endsWith(".forRoot")) ||
      (name.includes("RouterModule") && name.endsWith(".forChild"))
    ) {
      const args = node.getArguments();
      if (args.length === 0) return;
      const arr = resolveArrayLiteralArg(args[0], sf);
      if (arr) walkOnce(arr);
    }
  });

  for (const decl of sf.getVariableDeclarations()) {
    if (!ROUTES_VAR.test(decl.getName())) continue;
    const init = decl.getInitializer();
    if (!init?.isKind(SyntaxKind.ArrayLiteralExpression)) continue;
    walkOnce(init.asKindOrThrow(SyntaxKind.ArrayLiteralExpression));
  }
}
