import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileSolidRouter } from "./enrichers-solid-routes";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileSolidRouter", () => {
  it("emits PAGE_ROUTE for Solid Router JSX Route", () => {
    const source = `
import { Route } from '@solidjs/router';

export function App() {
  return (
    <>
      <Route path="/home" component={null} />
      <Route path="/app" component={null}>
        <Route path="sub" component={null} />
      </Route>
    </>
  );
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileSolidRouter(sf, entry, "src.app");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(patterns).toContain("/home");
    expect(patterns).toContain("/app");
    expect(patterns).toContain("/app/sub");
  });

  it("skips files without @solidjs/router import", () => {
    const source = `
import { Route } from 'other';
export function X() { return <Route path="/x" />; }
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("x.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileSolidRouter(sf, entry, "src.x");
    expect(entry.symbols.filter((s) => s.kind === "PAGE_ROUTE")).toHaveLength(0);
  });
});
