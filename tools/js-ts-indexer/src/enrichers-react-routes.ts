/**
 * React Router (v6+): JSX <Route>, createBrowserRouter([...]), createRoutesFromElements(<Route/>).
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import {
  enrichCreateRoutesFromElements,
  enrichJsxRouteElements,
  joinNestedRoutePaths,
  pageRouteFQName,
  resolveArrayLiteralArg,
  walkRouteConfigArray,
} from "./enrichers-page-route-common";

export { pageRouteFQName };
/** @deprecated use joinNestedRoutePaths — kept for existing tests */
export const joinReactRouterPaths = joinNestedRoutePaths;

const REACT_DATA_ROUTER_CREATORS = [
  "createBrowserRouter",
  "createHashRouter",
  "createMemoryRouter",
] as const;

/**
 * React Router data API: createBrowserRouter / createHashRouter / createMemoryRouter([{ path, children }, ...]).
 */
export function enrichDataRouterRouteConfigs(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const expr = node.getExpression();
    const name = expr.getText().replace(/\s/g, "");
    const hit = REACT_DATA_ROUTER_CREATORS.some((fn) => name === fn || name.endsWith("." + fn));
    if (!hit) return;
    const args = node.getArguments();
    if (args.length === 0) return;
    const first = args[0];
    const arr = resolveArrayLiteralArg(first, sf);
    if (!arr) return;
    walkRouteConfigArray(arr, entry, moduleFq, "", "react_router_data");
  });
}

/** @deprecated use enrichDataRouterRouteConfigs */
export function enrichCreateBrowserRouter(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  enrichDataRouterRouteConfigs(sf, entry, moduleFq);
}

/**
 * Index React Router routes: nested JSX <Route>, data router config, createRoutesFromElements.
 */
export function enrichFileReactRouter(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
): void {
  enrichJsxRouteElements(sf, entry, moduleFq, "react_router_jsx");
  enrichDataRouterRouteConfigs(sf, entry, moduleFq);
  enrichCreateRoutesFromElements(sf, entry, moduleFq, "react_router_elements");
}
