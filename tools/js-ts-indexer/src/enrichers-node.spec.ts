import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import type { PackageInfo } from "./discovery";
import type { FileSymbolsEdges } from "./enrichers";
import {
  enrichBuiltinImports,
  enrichBuiltinRequireCalls,
  enrichNodePackageSurface,
} from "./enrichers-node";

describe("enrichBuiltinImports", () => {
  it("records node: specifier and legacy fs import", () => {
    const source = `
import fs from 'node:fs';
import path from 'path';
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("a.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichBuiltinImports(sf, entry, "src.a");
    const kinds = entry.symbols.map((s) => s.kind);
    expect(kinds.filter((k) => k === "BUILTIN_MODULE_USE").length).toBe(2);
    const specs = entry.symbols.map((s) => (s.signature as { specifier: string }).specifier);
    expect(specs).toContain("node:fs");
    expect(specs).toContain("path");
  });
});

describe("enrichBuiltinRequireCalls", () => {
  it("detects require('fs')", () => {
    const source = `const fs = require('fs');`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("cjs.cjs", source);
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichBuiltinRequireCalls(sf, entry, "cjs");
    expect(entry.symbols.some((s) => s.kind === "BUILTIN_MODULE_USE")).toBe(true);
  });
});

describe("enrichNodePackageSurface", () => {
  it("marks entrypoint and CLI on matching paths", () => {
    const pkg: PackageInfo = {
      name: "demo-cli",
      rootDir: "/tmp",
      packageJsonPath: "/tmp/package.json",
      relativeDir: ".",
      hasTypescript: true,
      isWorkspaceRoot: true,
      dependencies: [],
      devDependencies: [],
      moduleKind: "commonjs",
      packageRole: "cli",
      sourceRoots: ["."],
      testRoots: [],
      scripts: {},
      mainEntry: "src/index.ts",
      binEntries: [{ command: "demo-cli", relPath: "src/cli.ts" }],
    };
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNodePackageSurface(entry, "src/index.ts", "src.index", pkg);
    expect(entry.symbols.some((s) => s.kind === "ENTRYPOINT")).toBe(true);
    const cliEntry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichNodePackageSurface(cliEntry, "src/cli.ts", "src.cli", pkg);
    expect(cliEntry.symbols.some((s) => s.kind === "CLI_COMMAND")).toBe(true);
  });
});
