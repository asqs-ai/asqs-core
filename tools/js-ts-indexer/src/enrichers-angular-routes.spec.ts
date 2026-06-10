import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileAngularRoutes } from "./enrichers-angular-routes";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileAngularRoutes", () => {
  it("indexes RouterModule.forRoot inline routes with nested children", () => {
    const source = `
import { RouterModule } from '@angular/router';
import { NgModule } from '@angular/core';

@NgModule({
  imports: [
    RouterModule.forRoot([
      { path: 'shop', children: [{ path: 'item', component: null }] },
    ]),
  ],
})
export class AppModule {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.module.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularRoutes(sf, entry, "src.app.module");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(patterns).toContain("/shop");
    expect(patterns).toContain("/shop/item");
  });

  it("dedupes the same routes array used by forRoot and a const", () => {
    const source = `
import { RouterModule } from '@angular/router';

const routes = [{ path: 'a', component: null }];

@NgModule({
  imports: [RouterModule.forChild(routes)],
})
export class FeatureModule {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("feat.module.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularRoutes(sf, entry, "src.feat");

    const routes = entry.symbols.filter((s) => s.kind === "PAGE_ROUTE");
    expect(routes.length).toBe(1);
    expect((routes[0].signature as { path_pattern?: string })?.path_pattern).toBe("/a");
  });

  it("indexes provideRouter([...])", () => {
    const source = `
import { provideRouter } from '@angular/router';

export const appProviders = [
  provideRouter([{ path: 'dash', component: null }]),
];
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("main.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularRoutes(sf, entry, "src.main");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(patterns).toContain("/dash");
  });
});
