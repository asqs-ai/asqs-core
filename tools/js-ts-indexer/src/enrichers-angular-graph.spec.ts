import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileAngularGraph } from "./enrichers-angular-graph";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileAngularGraph", () => {
  it("emits ANGULAR_COMPONENT and USES_TEMPLATE for templateUrl", () => {
    const source = `
import { Component } from '@angular/core';

@Component({
  selector: 'app-root',
  standalone: true,
  templateUrl: './app.component.html',
})
export class AppComponent {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.component.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularGraph(sf, entry, "src.app.component", "src.app.component");
    const comp = entry.symbols.find((s) => s.kind === "ANGULAR_COMPONENT");
    expect(comp).toBeDefined();
    expect(entry.edges.some((e) => e.edge_type === "USES_TEMPLATE" && e.callee_fq_name === "./app.component.html")).toBe(
      true,
    );
    expect(entry.symbols.some((s) => s.kind === "ANGULAR_TEMPLATE")).toBe(true);
  });

  it("emits ANGULAR_MODULE and MODULE_DECLARATIONS", () => {
    const source = `
import { NgModule } from '@angular/core';
import { AppComponent } from './app.component';

@NgModule({
  declarations: [AppComponent],
  imports: [],
  bootstrap: [AppComponent],
})
export class AppModule {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.module.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularGraph(sf, entry, "src.app.module", "src.app.module");
    expect(entry.symbols.some((s) => s.kind === "ANGULAR_MODULE")).toBe(true);
    expect(entry.edges.some((e) => e.edge_type === "MODULE_DECLARATIONS")).toBe(true);
    expect(entry.edges.some((e) => e.edge_type === "MODULE_BOOTSTRAP")).toBe(true);
  });
});
