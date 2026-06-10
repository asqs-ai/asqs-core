/**
 * Phase 5 — React: components (function, class, arrow/memo), hooks, context, props types, JSX graph.
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import { fqName } from "./normalize";
import type { FileSymbolsEdges } from "./enrichers";

const BUILTIN_REACT_HOOKS = new Set([
  "useState",
  "useEffect",
  "useContext",
  "useReducer",
  "useCallback",
  "useMemo",
  "useRef",
  "useImperativeHandle",
  "useLayoutEffect",
  "useDebugValue",
  "useDeferredValue",
  "useTransition",
  "useId",
  "useSyncExternalStore",
  "useInsertionEffect",
  "useOptimistic",
  "useActionState",
  "useFormStatus",
]);

function addRendersEdges(
  entry: FileSymbolsEdges,
  componentFq: string,
  bodyContainer: { getDescendantsOfKind: (k: number) => unknown[] },
): void {
  const jsxElements = bodyContainer.getDescendantsOfKind(SyntaxKind.JsxElement);
  for (const el of jsxElements) {
    const opening = (el as { getOpeningElement: () => { getTagNameNode: () => { getText: () => string } } }).getOpeningElement();
    const tagName = opening.getTagNameNode().getText();
    if (tagName && tagName[0] === tagName[0].toUpperCase()) {
      entry.edges.push({ caller_fq_name: componentFq, callee_fq_name: tagName, edge_type: "RENDERS" });
    }
  }
  const selfClosing = bodyContainer.getDescendantsOfKind(SyntaxKind.JsxSelfClosingElement);
  for (const el of selfClosing) {
    const tagName = (el as { getTagNameNode: () => { getText: () => string } }).getTagNameNode().getText();
    if (tagName && tagName[0] === tagName[0].toUpperCase()) {
      entry.edges.push({ caller_fq_name: componentFq, callee_fq_name: tagName, edge_type: "RENDERS" });
    }
    // Context.Provider
    if (tagName.includes(".") && tagName.endsWith(".Provider")) {
      const ctx = tagName.replace(/\.Provider$/, "");
      entry.edges.push({ caller_fq_name: componentFq, callee_fq_name: ctx, edge_type: "USES_CONTEXT" });
    }
  }
}

function hookNameFromCall(call: { getExpression: () => { getKind: () => number; getText: () => string; getName?: () => string; getExpression?: () => { getText: () => string } } }): string | undefined {
  const expr = call.getExpression();
  if (expr.getKind() === SyntaxKind.Identifier) {
    return expr.getText();
  }
  if (expr.getKind() === SyntaxKind.PropertyAccessExpression) {
    const name = expr.getName?.();
    if (name) return name;
  }
  return undefined;
}

function enrichHookAndContextUsage(
  entry: FileSymbolsEdges,
  componentFq: string,
  bodyContainer: { forEachDescendant: (cb: (n: import("ts-morph").Node) => void) => void },
): void {
  const seenHooks = new Set<string>();
  bodyContainer.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const name = hookNameFromCall(node);
    if (!name) return;
    if (name === "useContext") {
      const args = node.getArguments();
      const a0 = args[0];
      if (a0) {
        entry.edges.push({
          caller_fq_name: componentFq,
          callee_fq_name: a0.getText().trim(),
          edge_type: "USES_CONTEXT",
        });
      }
      return;
    }
    if (BUILTIN_REACT_HOOKS.has(name) && !seenHooks.has(name)) {
      seenHooks.add(name);
      entry.edges.push({
        caller_fq_name: componentFq,
        callee_fq_name: name,
        edge_type: "USES_HOOK",
      });
      entry.symbols.push({
        kind: "REACT_HOOK",
        fq_name: `REACT_HOOK:${componentFq}:${name}`,
        start_line: node.getStartLineNumber(),
        end_line: node.getEndLineNumber(),
        signature: { hook: name, component_fq: componentFq },
      });
      entry.edges.push({
        caller_fq_name: componentFq,
        callee_fq_name: `REACT_HOOK:${componentFq}:${name}`,
        edge_type: "CONTAINS",
      });
    }
  });
}

function propsTypeFromParameters(
  params: import("ts-morph").Node[],
): { text: string; edgeLabel: string } | undefined {
  if (params.length === 0) return undefined;
  const p0 = params[0] as { getTypeNode?: () => { getText: () => string } | undefined; getName?: () => string };
  const tn = p0.getTypeNode?.();
  if (tn) {
    const t = tn.getText().trim();
    if (t && !/^(any|unknown)$/i.test(t)) return { text: t, edgeLabel: t };
  }
  return undefined;
}

function bodyHasJsx(body: { getText: () => string } | undefined): boolean {
  if (!body) return false;
  const t = body.getText();
  return t.includes("<") && t.includes(">");
}

function extendsReactComponent(classDecl: {
  getHeritageClauses: () => { getTypeNodes: () => { getText: () => string }[] }[];
}): boolean {
  for (const h of classDecl.getHeritageClauses()) {
    for (const t of h.getTypeNodes()) {
      const txt = t.getText();
      if (/\b(Component|PureComponent)\b/.test(txt)) return true;
    }
  }
  return false;
}

function unwrapMemoOrForwardRef(
  init: import("ts-morph").Node | undefined,
): import("ts-morph").Node | undefined {
  if (!init || !init.isKind(SyntaxKind.CallExpression)) return init;
  const callee = init.getExpression().getText().replace(/\s/g, "");
  if (callee === "memo" || callee === "forwardRef" || callee === "React.memo" || callee === "React.forwardRef") {
    const a0 = init.getArguments()[0];
    return a0;
  }
  return init;
}

/** Peel nested `memo(forwardRef(...))` / `forwardRef(memo(...))` up to 8 levels. */
function unwrapMemoOrForwardRefDeep(init: import("ts-morph").Node | undefined): import("ts-morph").Node | undefined {
  let cur = init;
  for (let i = 0; i < 8; i++) {
    if (!cur || !cur.isKind(SyntaxKind.CallExpression)) break;
    const next = unwrapMemoOrForwardRef(cur);
    if (!next || next === cur) break;
    cur = next;
  }
  return cur;
}

/** JSX `onClick={…}` / `onSubmit={…}` → lightweight `CALLS` edges to expression text. */
function enrichJsxHandlerCalls(
  entry: FileSymbolsEdges,
  componentFq: string,
  bodyContainer: { forEachDescendant: (cb: (n: import("ts-morph").Node) => void) => void },
): void {
  bodyContainer.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.JsxAttribute)) return;
    const nameNode = node.getNameNode();
    const aname = nameNode.getText();
    if (!/^on[A-Z]\w*$/.test(aname)) return;
    const init = node.getInitializer();
    if (!init?.isKind(SyntaxKind.JsxExpression)) return;
    const expr = init.getExpression();
    if (!expr) return;
    const callee = expr.getText().trim();
    if (!callee || callee.length > 160 || /[\r\n]/.test(callee)) return;
    entry.edges.push({
      caller_fq_name: componentFq,
      callee_fq_name: callee,
      edge_type: "CALLS",
    });
  });
}

function registerReactComponent(
  entry: FileSymbolsEdges,
  moduleFq: string,
  fq: string,
  startLine: number,
  endLine: number,
  bodyForJsx: { getDescendantsOfKind: (k: number) => unknown[]; forEachDescendant: (cb: (n: import("ts-morph").Node) => void) => void },
  props?: { text: string; edgeLabel: string },
): void {
  entry.symbols.push({
    kind: "REACT_COMPONENT",
    fq_name: fq,
    start_line: startLine,
    end_line: endLine,
    ...(props ? { signature: { props_type: props.text } } : {}),
  });
  addRendersEdges(entry, fq, bodyForJsx);
  enrichHookAndContextUsage(entry, fq, bodyForJsx);
  enrichJsxHandlerCalls(entry, fq, bodyForJsx);
  if (props) {
    entry.edges.push({
      caller_fq_name: fq,
      callee_fq_name: props.edgeLabel,
      edge_type: "ACCEPTS_PROPS_TYPE",
    });
  }
}

/**
 * React component graph, hooks, context, and props (Phase 5).
 */
export function enrichFileReact(sf: SourceFile, entry: FileSymbolsEdges, moduleId: string): void {
  const moduleFq = moduleId;

  sf.forEachDescendant((node) => {
    if (node.isKind(SyntaxKind.FunctionDeclaration)) {
      const name = node.getName();
      if (!name) return;
      const body = node.getBody();
      if (!bodyHasJsx(body)) return;
      const fq = fqName(moduleId, name);
      const params = node.getParameters();
      const props = propsTypeFromParameters(params);
      registerReactComponent(entry, moduleFq, fq, node.getStartLineNumber(), node.getEndLineNumber(), node, props);
      return;
    }

    if (node.isKind(SyntaxKind.ClassDeclaration)) {
      const name = node.getName();
      if (!name || !extendsReactComponent(node)) return;
      if (!bodyHasJsx(node)) return;
      const fq = fqName(moduleId, name);
      registerReactComponent(entry, moduleFq, fq, node.getStartLineNumber(), node.getEndLineNumber(), node);
      return;
    }

    if (node.isKind(SyntaxKind.VariableDeclaration)) {
      const name = node.getName();
      if (typeof name !== "string") return;
      let init = node.getInitializer();
      init = unwrapMemoOrForwardRefDeep(init) as typeof init;
      if (!init || (!init.isKind(SyntaxKind.ArrowFunction) && !init.isKind(SyntaxKind.FunctionExpression))) {
        return;
      }
      const body = init.getBody();
      if (!bodyHasJsx(body)) return;
      const stmt = node.getParent()?.getParent();
      if (stmt && stmt.getKind() === SyntaxKind.VariableStatement && !(stmt as { isExported?: () => boolean }).isExported?.()) {
        return;
      }
      const fq = fqName(moduleId, name);
      const params = init.getParameters();
      let props = propsTypeFromParameters(params);
      if (!props) {
        const vtn = node.getTypeNode();
        if (vtn) {
          const text = vtn.getText();
          const m =
            text.match(/\bFC\s*<\s*([^>]+)\s*>/i) ||
            text.match(/\bFunctionComponent\s*<\s*([^>]+)\s*>/i);
          if (m) {
            const inner = m[1].trim();
            props = { text: inner, edgeLabel: inner };
          }
        }
      }
      registerReactComponent(
        entry,
        moduleFq,
        fq,
        node.getStartLineNumber(),
        node.getEndLineNumber(),
        init,
        props,
      );
    }
  });

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.VariableDeclaration)) return;
    const name = node.getName();
    if (typeof name !== "string") return;
    const init = node.getInitializer();
    if (!init || !init.isKind(SyntaxKind.CallExpression)) return;
    const callee = init.getExpression().getText().replace(/\s/g, "");
    if (callee !== "createContext" && callee !== "React.createContext") return;
    const ctxFq = `REACT_CONTEXT:${fqName(moduleId, name)}`;
    entry.symbols.push({
      kind: "REACT_CONTEXT",
      fq_name: ctxFq,
      start_line: node.getStartLineNumber(),
      end_line: node.getEndLineNumber(),
      signature: { variable: name, module_id: moduleId },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: ctxFq,
      edge_type: "CONTAINS",
    });
    const provFq = `REACT_PROVIDER:${fqName(moduleId, name)}`;
    entry.symbols.push({
      kind: "REACT_PROVIDER",
      fq_name: provFq,
      start_line: node.getStartLineNumber(),
      end_line: node.getEndLineNumber(),
      signature: { context: ctxFq },
    });
    entry.edges.push({
      caller_fq_name: ctxFq,
      callee_fq_name: provFq,
      edge_type: "DECLARES",
    });
  });

  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.FunctionDeclaration)) return;
    const name = node.getName();
    if (!name || !/^use[A-Z]/.test(name)) return;
    if (BUILTIN_REACT_HOOKS.has(name)) return;
    const body = node.getBody();
    if (!body) return;
    let usesHook = false;
    body.forEachDescendant((inner) => {
      if (!inner.isKind(SyntaxKind.CallExpression)) return;
      const hn = hookNameFromCall(inner);
      if (hn && BUILTIN_REACT_HOOKS.has(hn)) usesHook = true;
    });
    if (!usesHook) return;
    const fq = fqName(moduleId, name);
    const hookFq = `REACT_CUSTOM_HOOK:${fq}`;
    entry.symbols.push({
      kind: "REACT_CUSTOM_HOOK",
      fq_name: hookFq,
      start_line: node.getStartLineNumber(),
      end_line: node.getEndLineNumber(),
      signature: { function_fq: fq },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: hookFq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: hookFq,
      callee_fq_name: fq,
      edge_type: "DECLARES",
    });
  });
}
