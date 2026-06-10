import { describe, expect, it } from "vitest";
import { Project, SyntaxKind } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import {
  enrichCallableTypeRefs,
  enrichEnums,
  enrichReExports,
  enrichSameFileNamedExports,
  enrichTestBlocks,
  enrichTypeAliases,
  jsdocSummary,
  signatureWithJsdocForVariable,
  signatureWithMemberVisibility,
} from "./enrichers-semantics";

describe("signatureWithJsdocForVariable", () => {
  it("does not attach JSDoc to locals inside a function (misplaced comments)", () => {
    const source = `
export async function getUniqueSlug(title: string): Promise<string> {
  const baseSlug = title;
  /**
   * Tests for slug logic — must not become uniqueSlug's symbol doc.
   */
  let uniqueSlug = baseSlug;
  return uniqueSlug;
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("f.ts", source.trim());
    const inner = sf
      .getDescendantsOfKind(SyntaxKind.VariableDeclaration)
      .find((d) => d.getName() === "uniqueSlug");
    expect(inner).toBeDefined();
    expect(signatureWithJsdocForVariable(inner!)).toEqual({});
  });

  it("still attaches JSDoc to top-level exports", () => {
    const source = `
/** Module-level constant. */
export const ANSWER = 42;
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("c.ts", source.trim());
    const v = sf.getVariableDeclarationOrThrow("ANSWER");
    expect(signatureWithJsdocForVariable(v).jsdoc).toContain("Module-level constant");
  });
});

describe("signatureWithMemberVisibility", () => {
  it("marks ECMAScript # private methods as visibility private", () => {
    const source = `
class C {
  #secret(): void {}
  public pub(): void {}
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("c.ts", source.trim());
    const cls = sf.getClassOrThrow("C");
    const secret = cls.getMethod("#secret");
    const pub = cls.getMethod("pub");
    expect(secret).toBeDefined();
    expect(signatureWithMemberVisibility({}, secret!).visibility).toBe("private");
    expect((signatureWithMemberVisibility({}, secret!) as { exported: boolean }).exported).toBe(false);
    expect(signatureWithMemberVisibility({}, pub!).visibility).toBe("public");
  });
});

describe("jsdocSummary", () => {
  it("reads leading JSDoc on a function", () => {
    const source = `
/**
 * Computes total price.
 * @param x amount
 */
export function foo(x: number): number { return x; }
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("f.ts", source.trim());
    const fn = sf.getFunctionOrThrow("foo");
    expect(jsdocSummary(fn)).toContain("Computes total price");
  });
});

describe("enrichTypeAliases and enrichEnums", () => {
  it("emits TYPE_ALIAS and ENUM with ENUM_MEMBER", () => {
    const source = `
export type ID = string;
export enum Color { Red = 1, Blue = 2 }
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("t.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    const addContains = (a: string, b: string) => {
      entry.edges.push({ caller_fq_name: a, callee_fq_name: b, edge_type: "CONTAINS" });
    };
    enrichTypeAliases(sf, entry, "t", "t", addContains);
    enrichEnums(sf, entry, "t", "t", addContains);
    const kinds = entry.symbols.map((s) => s.kind).sort();
    expect(kinds).toContain("TYPE_ALIAS");
    expect(kinds).toContain("ENUM");
    expect(kinds.filter((k) => k === "ENUM_MEMBER").length).toBe(2);
  });
});

describe("enrichCallableTypeRefs / REFS_TYPE", () => {
  it("emits REFS_TYPE from checker-resolved types when imports resolve", () => {
    const source = `
import type { User } from './types';
export function f(u: User): Promise<number> { return Promise.resolve(1); }
`;
    const project = new Project({ useInMemoryFileSystem: true });
    project.createSourceFile("types.ts", "export interface User { id: string }\n");
    const sf = project.createSourceFile("f.ts", source.trim());
    const edges: FileSymbolsEdges["edges"] = [];
    enrichCallableTypeRefs("f.f", sf.getFunctionOrThrow("f"), edges);
    const refs = edges.filter((e) => e.edge_type === "REFS_TYPE");
    expect(refs.length).toBeGreaterThanOrEqual(2);
    const callees = refs.map((e) => e.callee_fq_name);
    expect(callees.some((c) => c === "User" || c.includes("User"))).toBe(true);
    expect(callees.some((c) => c.includes("Promise"))).toBe(true);
  });
});

describe("exports", () => {
  it("RE_EXPORTS and same-file named exports", () => {
    const source = `
export { a } from './other';
export * from './bar';
export { localFn };
function localFn() {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("e.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichReExports(sf, entry, "e");
    enrichSameFileNamedExports(sf, entry, "e", "e");
    expect(entry.edges.some((e) => e.edge_type === "RE_EXPORTS" && e.callee_fq_name.includes("@./other"))).toBe(
      true,
    );
    expect(entry.edges.some((e) => e.edge_type === "RE_EXPORTS" && e.callee_fq_name.startsWith("*@"))).toBe(true);
    expect(entry.edges.some((e) => e.edge_type === "EXPORTS" && e.callee_fq_name === "e.localFn")).toBe(true);
  });
});

describe("enrichTestBlocks", () => {
  it("emits TEST_BLOCK for it() and describe()", () => {
    const source = `
import { describe, it, expect } from 'vitest';
describe('suite', () => {
  it('case one', () => { expect(1).toBe(1); });
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("x.test.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichTestBlocks(sf, entry, "x.test");
    const blocks = entry.symbols.filter((s) => s.kind === "TEST_BLOCK");
    expect(blocks.length).toBeGreaterThanOrEqual(2);
    expect(blocks.some((b) => (b.signature as { block_kind?: string }).block_kind === "suite")).toBe(true);
    expect(blocks.some((b) => (b.signature as { block_kind?: string }).block_kind === "case")).toBe(true);
  });
});
