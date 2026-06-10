import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import {
  enrichFileNest,
  normalizeHttpPath,
  nestApiRouteFQName,
  type FileSymbolsEdges,
} from "./enrichers";

describe("normalizeHttpPath", () => {
  it("joins controller prefix and method path", () => {
    expect(normalizeHttpPath("api", "users")).toBe("/api/users");
  });
  it("collapses duplicate slashes", () => {
    expect(normalizeHttpPath("api/", "/users")).toBe("/api/users");
  });
  it("adds leading slash", () => {
    expect(normalizeHttpPath("", "health")).toBe("/health");
  });
});

describe("nestApiRouteFQName", () => {
  it("includes method and handler for uniqueness", () => {
    const fq = nestApiRouteFQName("GET", "/api/items", "src.app.Ctrl.list");
    expect(fq).toBe("API_ROUTE:GET:/api/items@src.app.Ctrl.list");
  });
});

describe("enrichFileNest", () => {
  it("emits API_ROUTE symbols and resolvable ROUTE_TO_HANDLER edges", () => {
    const source = `
import { Controller, Get } from '@nestjs/common';

@Controller('api/cats')
export class CatsController {
  @Get('profile')
  getProfile() {
    return {};
  }
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("cats.controller.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileNest(sf, entry, "src.cats.controller");

    const apiRoutes = entry.symbols.filter((s) => s.kind === "API_ROUTE");
    expect(apiRoutes.length).toBe(1);
    expect(apiRoutes[0].fq_name).toContain("API_ROUTE:GET:");
    expect(apiRoutes[0].fq_name).toContain("/api/cats/profile");
    expect(apiRoutes[0].signature).toMatchObject({
      framework: "nest",
      http_method: "GET",
      path_pattern: "/api/cats/profile",
    });

    const routeEdges = entry.edges.filter((e) => e.edge_type === "ROUTE_TO_HANDLER");
    expect(routeEdges.length).toBe(1);
    expect(routeEdges[0].caller_fq_name).toBe(apiRoutes[0].fq_name);
    expect(routeEdges[0].callee_fq_name).toBe("src.cats.controller.CatsController.getProfile");

    const contains = entry.edges.filter(
      (e) => e.edge_type === "CONTAINS" && e.callee_fq_name === apiRoutes[0].fq_name,
    );
    expect(contains.length).toBe(1);
  });
});
