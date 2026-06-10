/**
 * Map AST positions to 1-based columns for LangIndexerJSON symbols.
 *
 * Uses the TypeScript compiler's line/character model: "character" is a 0-based offset in UTF-16
 * code units on that line — the same basis as LSP {@link https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#position Position}.
 * We store **1-based** columns (first column on the line = 1), i.e. LSP character + 1.
 */

import type { Node } from "ts-morph";

/** Inclusive end column: last code unit inside [start, endExclusive). */
export function spanColumns1Based(node: Node, start: number, endExclusive: number): { start_column: number; end_column: number } {
  const sf = node.getSourceFile().compilerNode;
  const end = endExclusive > start ? endExclusive - 1 : start;
  const s = sf.getLineAndCharacterOfPosition(start);
  const e = sf.getLineAndCharacterOfPosition(end);
  return {
    start_column: s.character + 1,
    end_column: e.character + 1,
  };
}

export function spanColumnsForNode(node: Node): { start_column: number; end_column: number } {
  return spanColumns1Based(node, node.getStart(), node.getEnd());
}
