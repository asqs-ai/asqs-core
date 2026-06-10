import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichNestDtoGuardsPipes } from "./enrichers-nest-dto-guards";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichNestDtoGuardsPipes", () => {
  it("emits USES_GUARD, USES_PIPE, HANDLER_USES_DTO for controller methods", () => {
    const source = `
import { Body, Controller, Get, Post, UseGuards, UsePipes } from '@nestjs/common';
import { AuthGuard } from './auth.guard';
import { ZodPipe } from './zod.pipe';

class CreateCatDto { name!: string; }

@Controller('cats')
@UseGuards(AuthGuard)
export class CatsController {
  @Post()
  @UsePipes(ZodPipe)
  create(@Body() dto: CreateCatDto) {
    return dto;
  }

  @Get('ok')
  list() {
    return [];
  }
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("cats.controller.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNestDtoGuardsPipes(sf, entry, "src.cats.controller", "src.cats.controller");

    expect(entry.edges.some((e) => e.edge_type === "USES_GUARD" && e.caller_fq_name.includes("create"))).toBe(
      true,
    );
    expect(entry.edges.some((e) => e.edge_type === "USES_PIPE" && e.caller_fq_name.includes("create"))).toBe(true);
    expect(entry.edges.some((e) => e.edge_type === "HANDLER_USES_DTO" && e.callee_fq_name === "CreateCatDto")).toBe(
      true,
    );
    expect(entry.symbols.some((s) => s.kind === "DTO")).toBe(true);
    expect(entry.symbols.some((s) => s.kind === "NEST_GUARD")).toBe(true);
    expect(entry.symbols.some((s) => s.kind === "NEST_PIPE")).toBe(true);
  });

  it("emits USES_INTERCEPTOR when present", () => {
    const source = `
import { Controller, Get, UseInterceptors } from '@nestjs/common';
import { LogInterceptor } from './log.interceptor';

@Controller('x')
export class XController {
  @Get()
  @UseInterceptors(LogInterceptor)
  ping() { return 'pong'; }
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("x.controller.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNestDtoGuardsPipes(sf, entry, "x", "x");
    expect(entry.edges.some((e) => e.edge_type === "USES_INTERCEPTOR")).toBe(true);
    expect(entry.symbols.some((s) => s.kind === "NEST_INTERCEPTOR")).toBe(true);
  });
});
