/**
 * Core semantic enrichments for JS/TS: JSDoc on symbols, type aliases, enums (+ members),
 * export graph, lightweight type-reference edges, unit-test call blocks.
 */

import { TypeFormatFlags } from "typescript";
import { SyntaxKind } from "ts-morph";
import type {
  ArrowFunction,
  CallExpression,
  Expression,
  FunctionDeclaration,
  JSDocableNode,
  MethodDeclaration,
  MethodSignature,
  Node,
  ParameterDeclaration,
  SourceFile,
  VariableDeclaration,
} from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { fqName } from "./normalize";

const MAX_JS_DOC = 800;
const MAX_TYPE_REF_LEN = 240;

/** True when the node sits at module / namespace scope (not inside a function, class, or block). */
export function isTopLevelLike(node: Node): boolean {
  const p = node.getParent();
  if (!p) return false;
  if (p.getKind() === SyntaxKind.SourceFile) return true;
  if (p.getKind() === SyntaxKind.ModuleBlock) {
    const gp = p.getParent();
    return gp?.getKind() === SyntaxKind.ModuleDeclaration;
  }
  return false;
}

/** First paragraph of leading JSDoc / TSDoc for a node. */
export function jsdocSummary(node: JSDocableNode): string | undefined {
  try {
    const docs = node.getJsDocs();
    if (docs.length === 0) return undefined;
    const raw = docs[0].getDescription().trim();
    if (!raw) return undefined;
    const para = raw.split(/\n\s*\n/)[0] ?? raw;
    const oneLine = para.replace(/\s+/g, " ").trim();
    return oneLine.length > MAX_JS_DOC ? oneLine.slice(0, MAX_JS_DOC) + "…" : oneLine;
  } catch {
    return undefined;
  }
}

export function signatureWithJsdoc(base: Record<string, unknown>, node: JSDocableNode): Record<string, unknown> {
  const j = jsdocSummary(node);
  if (!j) return base;
  return { ...base, jsdoc: j };
}

/**
 * JSDoc usually hangs on `VariableStatement`, not each `VariableDeclaration`.
 * Only module-/namespace-level variables get indexed JSDoc: a comment before an inner `let`/`const`
 * (e.g. misplaced or copied test notes) must not become the doc for that local.
 */
export function signatureWithJsdocForVariable(decl: VariableDeclaration): Record<string, unknown> {
  const vs = decl.getVariableStatement();
  if (vs && isTopLevelLike(vs)) {
    return signatureWithJsdoc({}, vs);
  }
  return {};
}

/**
 * Module export surface for a named declaration (class, interface, variable, etc.).
 * Aligns with Java `exported` + coarse visibility for chunk_metadata merge.
 */
export function signatureWithNamedDeclarationExport(
  base: Record<string, unknown>,
  decl: { isExported(): boolean },
): Record<string, unknown> {
  const exported = decl.isExported();
  return { ...base, exported, visibility: exported ? "public" : "internal" };
}

/** Class/interface instance method: TS modifiers vs Java visibility + public API surface. */
export function signatureWithMemberVisibility(
  base: Record<string, unknown>,
  member: MethodDeclaration,
): Record<string, unknown> {
  const nameNode = member.getNameNode();
  const visibility =
    nameNode.getKind() === SyntaxKind.PrivateIdentifier
      ? "private"
      : member.hasModifier(SyntaxKind.PrivateKeyword)
        ? "private"
        : member.hasModifier(SyntaxKind.ProtectedKeyword)
          ? "protected"
          : "public";
  return { ...base, visibility, exported: visibility === "public" };
}

/** Interface methods are public contract surface. */
export function signatureWithInterfaceMethodSurface(
  base: Record<string, unknown>,
  _mem: MethodSignature,
): Record<string, unknown> {
  return { ...base, visibility: "public", exported: true };
}

/** Top-level function declaration. */
export function signatureWithFunctionExportSurface(
  base: Record<string, unknown>,
  fn: FunctionDeclaration,
): Record<string, unknown> {
  const exported = fn.isExported();
  return { ...base, visibility: exported ? "public" : "internal", exported };
}

function pushExportsEdge(moduleFq: string, symbolFq: string, edges: FileSymbolsEdges["edges"]): void {
  edges.push({
    caller_fq_name: moduleFq,
    callee_fq_name: symbolFq,
    edge_type: "EXPORTS",
  });
}

/** Default export: export default <expr>; */
export function enrichDefaultExports(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
): void {
  for (const ea of sf.getExportAssignments()) {
    const ex = ea.getExpression();
    const fq = defaultExportTargetFq(moduleId, ex);
    if (fq) {
      pushExportsEdge(moduleFq, fq, entry.edges);
    }
  }
}

function defaultExportTargetFq(moduleId: string, ex: Expression): string | undefined {
  if (ex.isKind(SyntaxKind.Identifier)) {
    return fqName(moduleId, ex.getText());
  }
  if (ex.isKind(SyntaxKind.CallExpression) || ex.isKind(SyntaxKind.PropertyAccessExpression)) {
    return `default:${ex.getText().slice(0, 120)}`;
  }
  return undefined;
}

/** `export { foo, bar }` without `from` (same-file re-export surface). */
export function enrichSameFileNamedExports(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
): void {
  for (const ed of sf.getExportDeclarations()) {
    if (ed.getModuleSpecifier()) {
      continue;
    }
    for (const ne of ed.getNamedExports()) {
      pushExportsEdge(moduleFq, fqName(moduleId, ne.getName()), entry.edges);
    }
  }
}

/** export { a, b as c } from 'mod' and export * from 'mod' */
export function enrichReExports(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  for (const ed of sf.getExportDeclarations()) {
    const spec = ed.getModuleSpecifierValue();
    if (!spec) continue;
    if (ed.isNamespaceExport()) {
      entry.edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: `*@${spec}`,
        edge_type: "RE_EXPORTS",
      });
      continue;
    }
    for (const ne of ed.getNamedExports()) {
      const name = ne.getName();
      entry.edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: `${name}@${spec}`,
        edge_type: "RE_EXPORTS",
      });
    }
  }
}

function addTypeRefEdges(ownerFq: string, edges: FileSymbolsEdges["edges"], typeText: string | undefined): void {
  const t = (typeText ?? "").trim();
  if (!t || t.length > MAX_TYPE_REF_LEN) return;
  if (t === "any" || t === "void" || t === "unknown" || t === "never") return;
  edges.push({
    caller_fq_name: ownerFq,
    callee_fq_name: t,
    edge_type: "REFS_TYPE",
  });
}

/** Prefer TS checker–resolved type text; fall back to AST `getText()`. */
function typeRefTextFromTypeNode(tn: Node): string {
  try {
    const t = tn.getType();
    const s = t.getText(tn, TypeFormatFlags.NoTruncation)?.trim();
    if (s) return s;
  } catch {
    // ignore
  }
  return tn.getText().trim();
}

function typeRefTextFromParameter(p: ParameterDeclaration): string | undefined {
  const tn = p.getTypeNode();
  if (tn) {
    try {
      const t = p.getType();
      const s = t.getText(p, TypeFormatFlags.NoTruncation)?.trim();
      if (s) return s;
    } catch {
      // ignore
    }
    return tn.getText().trim();
  }
  try {
    const t = p.getType();
    const s = t.getText(p, TypeFormatFlags.NoTruncation)?.trim();
    if (s) return s;
  } catch {
    // ignore
  }
  return undefined;
}

export function enrichCallableTypeRefs(
  ownerFq: string,
  node: MethodDeclaration | FunctionDeclaration | ArrowFunction,
  edges: FileSymbolsEdges["edges"],
): void {
  try {
    const ret = node.getReturnTypeNode();
    if (ret) addTypeRefEdges(ownerFq, edges, typeRefTextFromTypeNode(ret));
    for (const p of node.getParameters()) {
      const text = typeRefTextFromParameter(p);
      if (text) addTypeRefEdges(ownerFq, edges, text);
    }
  } catch {
    // ignore
  }
}

export function enrichInterfaceMemberTypeRefs(
  ifaceFq: string,
  sig: MethodSignature,
  edges: FileSymbolsEdges["edges"],
): void {
  const name = sig.getName();
  const memberFq = `${ifaceFq}.${name}`;
  try {
    const ret = sig.getReturnTypeNode();
    if (ret) addTypeRefEdges(memberFq, edges, typeRefTextFromTypeNode(ret));
    for (const p of sig.getParameters()) {
      const text = typeRefTextFromParameter(p);
      if (text) addTypeRefEdges(memberFq, edges, text);
    }
  } catch {
    // ignore
  }
}

/** Top-level type aliases (in source file or namespace block). */
export function enrichTypeAliases(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
  addContains: (caller: string, callee: string) => void,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.TypeAliasDeclaration)) return;
    if (!isTopLevelLike(node)) return;
    const name = node.getName();
    const start = node.getStartLineNumber();
    const end = node.getEndLineNumber();
    const symFq = fqName(moduleId, name);
    const typeText = node.getTypeNode()?.getText() ?? "";
    entry.symbols.push({
      kind: "TYPE_ALIAS",
      fq_name: symFq,
      start_line: start,
      end_line: end,
      signature: signatureWithNamedDeclarationExport(
        signatureWithJsdoc({ type: typeText.slice(0, MAX_TYPE_REF_LEN) }, node),
        node,
      ),
    });
    addContains(moduleFq, symFq);
    if (node.isExported()) {
      pushExportsEdge(moduleFq, symFq, entry.edges);
    }
  });
}

/** Top-level enums and ENUM_MEMBER children. */
export function enrichEnums(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleId: string,
  moduleFq: string,
  addContains: (caller: string, callee: string) => void,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.EnumDeclaration)) return;
    if (!isTopLevelLike(node)) return;
    const name = node.getName();
    const start = node.getStartLineNumber();
    const end = node.getEndLineNumber();
    const enumFq = fqName(moduleId, name);
    entry.symbols.push({
      kind: "ENUM",
      fq_name: enumFq,
      start_line: start,
      end_line: end,
      signature: signatureWithNamedDeclarationExport(signatureWithJsdoc({}, node), node),
    });
    addContains(moduleFq, enumFq);
    if (node.isExported()) {
      pushExportsEdge(moduleFq, enumFq, entry.edges);
    }
    for (const member of node.getMembers()) {
      const mn = member.getName();
      const mFq = `${enumFq}.${mn}`;
      const ms = member.getStartLineNumber();
      const me = member.getEndLineNumber();
      entry.symbols.push({
        kind: "ENUM_MEMBER",
        fq_name: mFq,
        start_line: ms,
        end_line: me,
      });
      addContains(enumFq, mFq);
    }
  });
}

function callCalleeRootName(expr: Expression): string {
  if (expr.isKind(SyntaxKind.Identifier)) {
    return expr.getText();
  }
  if (expr.isKind(SyntaxKind.PropertyAccessExpression)) {
    return expr.getName();
  }
  return "";
}

function firstStringLiteralArg(call: CallExpression): string | undefined {
  const args = call.getArguments();
  if (args.length === 0) return undefined;
  const a0 = args[0];
  if (a0.isKind(SyntaxKind.StringLiteral)) {
    return a0.getLiteralText();
  }
  if (a0.isKind(SyntaxKind.NoSubstitutionTemplateLiteral)) {
    return a0.getLiteralText();
  }
  return undefined;
}

/**
 * Vitest/Jest/Mocha-style test blocks: test(...), it(...), describe(...), test.describe(...).
 */
export function enrichTestBlocks(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const call = node;
    const expr = call.getExpression();
    const root = callCalleeRootName(expr);
    let framework = "test";
    let blockKind = "case";
    if (root === "describe" || root === "suite") {
      blockKind = "suite";
      if (root === "suite") framework = "mocha";
    } else if (root === "test" || root === "it") {
      blockKind = "case";
    } else if (expr.isKind(SyntaxKind.PropertyAccessExpression)) {
      const left = expr.getExpression();
      if (left.isKind(SyntaxKind.Identifier) && left.getText() === "test" && expr.getName() === "describe") {
        blockKind = "suite";
        framework = "vitest";
      } else {
        return;
      }
    } else {
      return;
    }
    const title = firstStringLiteralArg(call);
    if (!title) return;
    const line = call.getStartLineNumber();
    const slug = title
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "_")
      .replace(/^_|_$/g, "")
      .slice(0, 80);
    const fq = `TEST_BLOCK:${blockKind}:${slug || "untitled"}@${moduleFq}:L${line}`;
    entry.symbols.push({
      kind: "TEST_BLOCK",
      fq_name: fq,
      start_line: line,
      end_line: call.getEndLineNumber(),
      signature: { framework, block_kind: blockKind, title: title.slice(0, 200) },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: fq,
      edge_type: "CONTAINS",
    });
  });
}
