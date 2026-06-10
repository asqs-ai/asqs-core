/**
 * Shared PAGE_ROUTE extraction: path normalization, config objects (path + children),
 * and JSX <Route> handling with nested path joining.
 */

import { SyntaxKind } from "ts-morph";
import type {
  ArrayLiteralExpression,
  JsxOpeningElement,
  JsxSelfClosingElement,
  Node,
  ObjectLiteralExpression,
  SourceFile,
} from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { normalizeHttpPath } from "./enrichers";

export function pageRouteFQName(pathPattern: string, moduleFq: string, line: number): string {
  const p = normalizeHttpPath("", pathPattern);
  return `PAGE_ROUTE:${p}@${moduleFq}:L${line}`;
}

/** JSX route components: exact `Route`, or `*Route` wrappers (PrivateRoute), but not `*Router` (BrowserRouter). */
export function isRouteLikeJsxTag(tagName: string): boolean {
  const t = tagName.trim();
  if (t === "Route") return true;
  if (t.endsWith("Router")) return false;
  return t.endsWith("Route");
}

/** Join parent route prefix with child segment (relative segments append; absolute replaces). */
export function joinNestedRoutePaths(parentPrefix: string, segment: string): string {
  const s = segment.trim();
  if (s === "") {
    return parentPrefix.trim() === "" ? "/" : normalizeHttpPath("", parentPrefix.trim());
  }
  if (s.startsWith("/")) {
    return normalizeHttpPath("", s);
  }
  const p = parentPrefix.trim();
  if (p === "") {
    return normalizeHttpPath("", s);
  }
  const base = p.endsWith("/") ? p.slice(0, -1) : p;
  return normalizeHttpPath(base, s);
}

export function pushPageRoute(
  entry: FileSymbolsEdges,
  moduleFq: string,
  pathVal: string,
  startLine: number,
  endLine: number,
  framework: string,
): void {
  const fq = pageRouteFQName(pathVal, moduleFq, startLine);
  const pattern = normalizeHttpPath("", pathVal);
  entry.symbols.push({
    kind: "PAGE_ROUTE",
    fq_name: fq,
    start_line: startLine,
    end_line: endLine,
    signature: { framework, path_pattern: pattern },
  });
  entry.edges.push({
    caller_fq_name: moduleFq,
    callee_fq_name: fq,
    edge_type: "CONTAINS",
  });
}

export function walkRouteConfigObject(
  obj: ObjectLiteralExpression,
  entry: FileSymbolsEdges,
  moduleFq: string,
  parentPrefix: string,
  framework: string,
): void {
  let pathSegment = "";
  let pathStartLine = 1;
  let pathEndLine = 1;
  let childrenArray: ArrayLiteralExpression | undefined;

  for (const prop of obj.getProperties()) {
    if (!prop.isKind(SyntaxKind.PropertyAssignment)) continue;
    const pname = prop.getName();
    if (pname === "path") {
      const init = prop.getInitializer();
      if (init?.isKind(SyntaxKind.StringLiteral)) {
        pathSegment = init.getLiteralText();
        pathStartLine = prop.getStartLineNumber();
        pathEndLine = prop.getEndLineNumber();
      }
    } else if (pname === "children") {
      const init = prop.getInitializer();
      if (init?.isKind(SyntaxKind.ArrayLiteralExpression)) {
        childrenArray = init.asKindOrThrow(SyntaxKind.ArrayLiteralExpression);
      }
    }
  }

  let thisFull = "";
  if (pathSegment !== "") {
    thisFull = joinNestedRoutePaths(parentPrefix, pathSegment);
    pushPageRoute(entry, moduleFq, thisFull, pathStartLine, pathEndLine, framework);
  }
  const childParent = thisFull !== "" ? thisFull : parentPrefix;
  if (childrenArray) {
    for (const el of childrenArray.getElements()) {
      if (el.isKind(SyntaxKind.ObjectLiteralExpression)) {
        walkRouteConfigObject(
          el.asKindOrThrow(SyntaxKind.ObjectLiteralExpression),
          entry,
          moduleFq,
          childParent,
          framework,
        );
      }
    }
  }
}

export function walkRouteConfigArray(
  arr: ArrayLiteralExpression,
  entry: FileSymbolsEdges,
  moduleFq: string,
  parentPrefix: string,
  framework: string,
): void {
  for (const el of arr.getElements()) {
    if (el.isKind(SyntaxKind.ObjectLiteralExpression)) {
      walkRouteConfigObject(
        el.asKindOrThrow(SyntaxKind.ObjectLiteralExpression),
        entry,
        moduleFq,
        parentPrefix,
        framework,
      );
    }
  }
}

/** Read `path` from <Route path="..." />; undefined if missing. */
/** True if this node is inside the first argument of a `createRoutesFromElements(...)` call. */
export function isUnderCreateRoutesFromElementsCall(node: Node): boolean {
  let p: Node | undefined = node.getParent();
  while (p) {
    if (p.isKind(SyntaxKind.CallExpression)) {
      const name = p.getExpression().getText().replace(/\s/g, "");
      if (name === "createRoutesFromElements" || name.endsWith(".createRoutesFromElements")) {
        return true;
      }
    }
    p = p.getParent();
  }
  return false;
}

export function readJsxRoutePath(el: JsxOpeningElement | JsxSelfClosingElement): string | undefined {
  const attr = el.getAttribute("path");
  if (!attr || attr.getKind() !== SyntaxKind.JsxAttribute) {
    return undefined;
  }
  const jsxAttr = attr.asKindOrThrow(SyntaxKind.JsxAttribute);
  const init = jsxAttr.getInitializer();
  if (!init) {
    return undefined;
  }
  if (init.isKind(SyntaxKind.StringLiteral)) {
    return init.getLiteralText();
  }
  if (init.isKind(SyntaxKind.JsxExpression)) {
    const expr = init.getExpression();
    if (expr?.isKind(SyntaxKind.StringLiteral)) {
      return expr.getLiteralText();
    }
    if (expr?.isKind(SyntaxKind.NoSubstitutionTemplateLiteral)) {
      return expr.getLiteralValue();
    }
  }
  return undefined;
}

/**
 * Prefix from ancestor <Route> elements in the JSX tree (not including `routeNode` itself).
 */
export function ancestorRoutePrefix(routeNode: JsxOpeningElement | JsxSelfClosingElement): string {
  const segments: string[] = [];
  let p: Node | undefined = routeNode.getParent();
  while (p) {
    if (p.isKind(SyntaxKind.JsxElement)) {
      const open = p.getOpeningElement();
      if (open === routeNode) {
        p = p.getParent();
        continue;
      }
      if (isRouteLikeJsxTag(open.getTagNameNode().getText())) {
        const pv = readJsxRoutePath(open);
        if (pv !== undefined && pv !== "") {
          segments.unshift(pv);
        }
      }
    }
    p = p.getParent();
  }
  return segments.reduce((acc, seg) => joinNestedRoutePaths(acc, seg), "");
}

/**
 * All <Route> in the file with nested path joining from JSX ancestry (react-router-dom, @solidjs/router, etc.).
 */
export function enrichJsxRouteElements(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
  framework: string,
): void {
  for (const el of sf.getDescendantsOfKind(SyntaxKind.JsxSelfClosingElement)) {
    if (!isRouteLikeJsxTag(el.getTagNameNode().getText())) continue;
    if (isUnderCreateRoutesFromElementsCall(el)) continue;
    const pathVal = readJsxRoutePath(el);
    if (pathVal === undefined) continue;
    const prefix = ancestorRoutePrefix(el);
    const full = joinNestedRoutePaths(prefix, pathVal);
    pushPageRoute(entry, moduleFq, full, el.getStartLineNumber(), el.getEndLineNumber(), framework);
  }

  for (const jsxEl of sf.getDescendantsOfKind(SyntaxKind.JsxElement)) {
    const open = jsxEl.getOpeningElement();
    if (!isRouteLikeJsxTag(open.getTagNameNode().getText())) continue;
    if (isUnderCreateRoutesFromElementsCall(open)) continue;
    const pathVal = readJsxRoutePath(open);
    if (pathVal === undefined) continue;
    const prefix = ancestorRoutePrefix(open);
    const full = joinNestedRoutePaths(prefix, pathVal);
    pushPageRoute(entry, moduleFq, full, open.getStartLineNumber(), open.getEndLineNumber(), framework);
  }
}

/** Unwrap `as const`, `as Foo[]`, `satisfies T` to reach an array literal. */
function arrayLiteralFromInitializer(init: Node | undefined): ArrayLiteralExpression | undefined {
  if (!init) return undefined;
  if (init.isKind(SyntaxKind.ArrayLiteralExpression)) {
    return init.asKindOrThrow(SyntaxKind.ArrayLiteralExpression);
  }
  if (init.isKind(SyntaxKind.AsExpression)) {
    return arrayLiteralFromInitializer(init.getExpression());
  }
  if (init.isKind(SyntaxKind.SatisfiesExpression)) {
    return arrayLiteralFromInitializer(
      init.asKindOrThrow(SyntaxKind.SatisfiesExpression).getExpression(),
    );
  }
  return undefined;
}

/**
 * Resolve `routes` array passed as identifier in the same file (best-effort).
 * Handles `createBrowserRouter(ROUTES)` when `ROUTES` is `const ROUTES = [...]` or `= [...] as const`.
 */
export function resolveArrayLiteralArg(arg: Node, sf: SourceFile): ArrayLiteralExpression | undefined {
  if (arg.isKind(SyntaxKind.ArrayLiteralExpression)) {
    return arg.asKindOrThrow(SyntaxKind.ArrayLiteralExpression);
  }
  if (arg.isKind(SyntaxKind.Identifier)) {
    const name = arg.getText();
    for (const decl of sf.getVariableDeclarations()) {
      if (decl.getName() !== name) continue;
      const hit = arrayLiteralFromInitializer(decl.getInitializer());
      if (hit) return hit;
    }
    // `import { ROUTES } from './routes'; createBrowserRouter(ROUTES)` — follow binding to the other file.
    const sym = arg.getSymbol();
    if (sym) {
      const aliased = sym.getAliasedSymbol?.() ?? sym;
      const vd = aliased.getValueDeclaration();
      if (vd?.isKind(SyntaxKind.VariableDeclaration)) {
        return arrayLiteralFromInitializer(vd.asKindOrThrow(SyntaxKind.VariableDeclaration).getInitializer());
      }
    }
  }
  return undefined;
}

/**
 * `createRoutesFromElements(<Route>...</Route>)` — walk only the argument subtree with explicit prefix stacking.
 */
export function enrichCreateRoutesFromElements(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
  framework: string,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const name = node.getExpression().getText().replace(/\s/g, "");
    if (name !== "createRoutesFromElements" && !name.endsWith(".createRoutesFromElements")) return;
    const args = node.getArguments();
    if (args.length === 0) return;
    walkRouteJsxSubtree(args[0], entry, moduleFq, "", framework);
  });
}

function walkRouteJsxSubtree(
  node: Node,
  entry: FileSymbolsEdges,
  moduleFq: string,
  parentPrefix: string,
  framework: string,
): void {
  if (node.isKind(SyntaxKind.JsxElement)) {
    const open = node.getOpeningElement();
    const tag = open.getTagNameNode().getText();
    if (isRouteLikeJsxTag(tag)) {
      const pathVal = readJsxRoutePath(open);
      let nextPrefix = parentPrefix;
      if (pathVal !== undefined) {
        const full = joinNestedRoutePaths(parentPrefix, pathVal);
        pushPageRoute(entry, moduleFq, full, open.getStartLineNumber(), open.getEndLineNumber(), framework);
        nextPrefix = full;
      }
      for (const child of node.getJsxChildren()) {
        walkJsxChildForRoutes(child, entry, moduleFq, nextPrefix, framework);
      }
      return;
    }
    for (const child of node.getJsxChildren()) {
      walkJsxChildForRoutes(child, entry, moduleFq, parentPrefix, framework);
    }
    return;
  }

  if (node.isKind(SyntaxKind.JsxSelfClosingElement)) {
    if (isRouteLikeJsxTag(node.getTagNameNode().getText())) {
      const pathVal = readJsxRoutePath(node);
      if (pathVal !== undefined) {
        const full = joinNestedRoutePaths(parentPrefix, pathVal);
        pushPageRoute(entry, moduleFq, full, node.getStartLineNumber(), node.getEndLineNumber(), framework);
      }
    }
    return;
  }

  if (node.isKind(SyntaxKind.JsxFragment)) {
    node.forEachChild((c) => {
      walkRouteJsxSubtree(c, entry, moduleFq, parentPrefix, framework);
    });
    return;
  }

  if (node.isKind(SyntaxKind.JsxExpression)) {
    const ex = node.getExpression();
    if (ex) walkRouteJsxSubtree(ex, entry, moduleFq, parentPrefix, framework);
    return;
  }

  if (node.isKind(SyntaxKind.ParenthesizedExpression)) {
    const ex = node.getExpression();
    if (ex) walkRouteJsxSubtree(ex, entry, moduleFq, parentPrefix, framework);
  }
}

function walkJsxChildForRoutes(
  child: Node,
  entry: FileSymbolsEdges,
  moduleFq: string,
  parentPrefix: string,
  framework: string,
): void {
  if (child.getKind() === SyntaxKind.JsxText && !child.getText().trim()) {
    return;
  }
  walkRouteJsxSubtree(child, entry, moduleFq, parentPrefix, framework);
}
