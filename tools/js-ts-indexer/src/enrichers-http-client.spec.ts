import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileHttpClient } from "./enrichers-http-client";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileHttpClient", () => {
  it("indexes fetch('/api/x') as API_CLIENT_REQUEST + CALLS_API", () => {
    const source = `
export async function loadData() {
  const r = await fetch('/api/items');
  return r.json();
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("api.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileHttpClient(sf, entry, "src.api");

    const sym = entry.symbols.find((s) => s.kind === "API_CLIENT_REQUEST");
    expect(sym).toBeDefined();
    expect(sym!.fq_name).toContain("GET:");
    expect(sym!.fq_name).toContain("/api/items");
    expect(sym!.signature).toMatchObject({ framework: "fetch", http_method: "GET" });
    const edge = entry.edges.find((e) => e.edge_type === "CALLS_API");
    expect(edge).toBeDefined();
    expect(edge!.callee_fq_name).toBe(sym!.fq_name);
    expect(edge!.caller_fq_name).toContain("loadData");
  });

  it("indexes axios.post('/api/x')", () => {
    const source = `
import axios from 'axios';
export function save() {
  return axios.post('/api/save', {});
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("save.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileHttpClient(sf, entry, "src.save");

    const sym = entry.symbols.find((s) => s.kind === "API_CLIENT_REQUEST");
    expect(sym).toBeDefined();
    expect(sym!.signature).toMatchObject({ framework: "axios", http_method: "POST" });
  });

  it("indexes $fetch('/api/x') when allowTestChains", () => {
    const source = `
export async function t() {
  await $fetch('/api/health');
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("e2e.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileHttpClient(sf, entry, "tests.e2e", { allowTestChains: true });
    const sym = entry.symbols.find((s) => s.kind === "API_CLIENT_REQUEST");
    expect(sym).toBeDefined();
    expect(sym!.signature).toMatchObject({ framework: "ofetch", http_method: "GET" });
    expect(sym!.fq_name).toContain("/api/health");
  });

  it("indexes supertest-style .get('/api') when allowTestChains", () => {
    const source = `
import request from 'supertest';
it('x', () => request(app).get('/api/v1'));
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("api.it.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileHttpClient(sf, entry, "src.api.it", { allowTestChains: true });
    const sym = entry.symbols.find((s) => s.kind === "API_CLIENT_REQUEST");
    expect(sym).toBeDefined();
    expect(sym!.signature).toMatchObject({ framework: "http_chain_test", http_method: "GET" });
    expect(sym!.fq_name).toContain("/api/v1");
  });
});
