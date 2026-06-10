import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileReact } from "./enrichers-react-graph";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileReact (Phase 5)", () => {
  it("emits USES_HOOK and REACT_HOOK for useState inside a component", () => {
    const source = `
export function Counter() {
  const [n, setN] = useState(0);
  return <button type="button">{n}</button>;
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("c.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReact(sf, entry, "src.c");
    expect(entry.symbols.some((s) => s.kind === "REACT_COMPONENT")).toBe(true);
    expect(entry.edges.some((e) => e.edge_type === "USES_HOOK" && e.callee_fq_name === "useState")).toBe(true);
  });

  it("emits REACT_CONTEXT and REACT_PROVIDER for createContext", () => {
    const source = `
import { createContext } from 'react';
export const ThemeCtx = createContext('light');
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("ctx.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReact(sf, entry, "src.ctx");
    expect(entry.symbols.some((s) => s.kind === "REACT_CONTEXT")).toBe(true);
    expect(entry.symbols.some((s) => s.kind === "REACT_PROVIDER")).toBe(true);
  });

  it("detects class component extending React.Component", () => {
    const source = `
import { Component } from 'react';
export class Box extends Component {
  render() {
    return <div className="box" />;
  }
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("box.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReact(sf, entry, "src.box");
    expect(entry.symbols.some((s) => s.kind === "REACT_COMPONENT")).toBe(true);
  });
});
