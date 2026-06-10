import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichNestModuleGraph } from "./enrichers-nest-graph";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichNestModuleGraph", () => {
  it("emits NEST_MODULE and MODULE_* edges from @Module", () => {
    const source = `
import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { AppService } from './app.service';

@Module({
  imports: [],
  controllers: [AppController],
  providers: [AppService],
  exports: [AppService],
})
export class AppModule {}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.module.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNestModuleGraph(sf, entry, "src.app.module", "src.app.module");

    const mod = entry.symbols.find((s) => s.kind === "NEST_MODULE");
    expect(mod).toBeDefined();
    expect(mod!.fq_name).toBe("NEST_MODULE:src.app.module.AppModule");

    const types = entry.edges.map((e) => e.edge_type);
    expect(types.filter((t) => t === "MODULE_REGISTERS").length).toBe(1);
    expect(types.filter((t) => t === "MODULE_PROVIDES").length).toBe(1);
    expect(types.filter((t) => t === "MODULE_EXPORTS").length).toBe(1);
  });

  it("emits NEST_PROVIDER and INJECTS for @Injectable constructor params", () => {
    const source = `
import { Injectable } from '@nestjs/common';
import { CatsRepo } from './cats.repo';

@Injectable()
export class CatsService {
  constructor(private readonly repo: CatsRepo) {}
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("cats.service.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNestModuleGraph(sf, entry, "src.cats.service", "src.cats.service");

    const prov = entry.symbols.find((s) => s.kind === "NEST_PROVIDER");
    expect(prov).toBeDefined();
    const injects = entry.edges.filter((e) => e.edge_type === "INJECTS");
    expect(injects.some((e) => e.callee_fq_name === "CatsRepo")).toBe(true);
  });
});
