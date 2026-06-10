import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichTanStackFileRoutes } from "./enrichers-tanstack-router";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichTanStackFileRoutes", () => {
  it("indexes createFileRoute('/path')({ ... }) as PAGE_ROUTE", () => {
    const source = `
import { createFileRoute } from '@tanstack/react-router';
function About() { return null; }
export const Route = createFileRoute('/about')({
  component: About,
});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes/about.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichTanStackFileRoutes(sf, entry, "routes.about");

    const pr = entry.symbols.find((s) => s.kind === "PAGE_ROUTE");
    expect(pr).toBeDefined();
    expect(pr!.fq_name).toContain("/about");
    expect((pr!.signature as { framework?: string })?.framework).toBe("tanstack_router");
  });
});
