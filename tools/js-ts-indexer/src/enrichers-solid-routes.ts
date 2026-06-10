/**
 * Solid Router (@solidjs/router): JSX <Route path="..." /> (same shape as react-router).
 */

import type { SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { enrichJsxRouteElements } from "./enrichers-page-route-common";

function fileMentionsSolidRouter(sf: SourceFile): boolean {
  const t = sf.getFullText();
  return (
    /from\s+['"]@solidjs\/router['"]/.test(t) ||
    /require\s*\(\s*['"]@solidjs\/router['"]\s*\)/.test(t)
  );
}

export function enrichFileSolidRouter(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  if (!fileMentionsSolidRouter(sf)) return;
  enrichJsxRouteElements(sf, entry, moduleFq, "solid_router_jsx");
}
