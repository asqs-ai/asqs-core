import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileVueRouter } from "./enrichers-vue-routes";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileVueRouter", () => {
  it("indexes createRouter({ routes: [...] })", () => {
    const source = `
import { createRouter, createWebHistory } from 'vue-router';

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/about', component: {} },
    { path: '/app', children: [{ path: 'prefs', component: {} }] },
  ],
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("router.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileVueRouter(sf, entry, "src.router");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(patterns).toContain("/about");
    expect(patterns).toContain("/app");
    expect(patterns).toContain("/app/prefs");
  });

  it("does nothing without a vue-router import", () => {
    const source = `
import { something } from 'other-pkg';
export const router = createRouter({ routes: [] });
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("x.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileVueRouter(sf, entry, "src.x");
    expect(entry.symbols.filter((s) => s.kind === "PAGE_ROUTE")).toHaveLength(0);
  });

  it("indexes new VueRouter({ routes }) for Vue 2 style", () => {
    const source = `
import VueRouter from 'vue-router';

export default new VueRouter({
  routes: [{ path: '/legacy', component: {} }],
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("router.js", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileVueRouter(sf, entry, "src.router");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(patterns).toContain("/legacy");
  });
});
