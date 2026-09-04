import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { discoverProject } from "./discovery";
import { indexProjectStreaming, type LangIndexerJSON } from "./language-indexer";

// The metadata store held zero CALLS edges for the Angular and React fixtures on 2026-09-03: every
// callee was emitted as source text (`this.catalog.search`) that the Go ingester could not resolve
// to a symbol, so every unit gap was retrieved with deps_count: 0. Callees that bind to a repository
// declaration must now be emitted under the FQ name the symbol emitters give that declaration.
describe("indexProjectStreaming CALLS edges", () => {
  function withProject(files: Record<string, string>, fn: (entries: LangIndexerJSON[]) => void): void {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "jst-calls-"));
    try {
      fs.writeFileSync(
        path.join(dir, "tsconfig.json"),
        JSON.stringify({
          compilerOptions: { target: "ES2020", module: "ESNext", moduleResolution: "node", strict: true },
          include: ["src/**/*.ts"],
        }),
      );
      fs.writeFileSync(path.join(dir, "package.json"), JSON.stringify({ name: "t", private: true }));
      for (const [rel, body] of Object.entries(files)) {
        const full = path.join(dir, rel);
        fs.mkdirSync(path.dirname(full), { recursive: true });
        fs.writeFileSync(full, body);
      }
      const entries: LangIndexerJSON[] = [];
      indexProjectStreaming(dir, discoverProject(dir), { frameworks: "none" }, (e) => entries.push(e));
      fn(entries);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }

  const calls = (entries: LangIndexerJSON[], file: string, caller: string) =>
    entries
      .find((e) => e.path === file)!
      .edges.filter((e) => e.edge_type === "CALLS" && e.caller_fq_name === caller)
      .map((e) => e.callee_fq_name);

  it("resolves method, function and same-class callees to indexed FQ names", () => {
    withProject(
      {
        "src/app/catalog.service.ts": "export class CatalogService {\n  search(q: string): string[] { return [q]; }\n}\n",
        "src/app/util.ts": "export function helper(): number { return 1; }\n",
        "src/app/catalog.component.ts":
          "import { CatalogService } from './catalog.service';\nimport { helper } from './util';\n\n" +
          "export class CatalogComponent {\n  private readonly catalog = new CatalogService();\n" +
          "  run(): void {\n    this.catalog.search('x');\n    helper();\n    this.reset();\n    console.log('done');\n  }\n" +
          "  private reset(): void {}\n}\n",
      },
      (entries) => {
        const callees = calls(entries, "src/app/catalog.component.ts", "src.app.catalog.component.CatalogComponent.run");
        expect(callees).toContain("src.app.catalog.service.CatalogService.search");
        expect(callees).toContain("src.app.util.helper");
        expect(callees).toContain("src.app.catalog.component.CatalogComponent.reset");
        // A library call keeps its raw text (the ingester drops it, as before); no invented FQ name.
        expect(callees).toContain("console.log");
        expect(callees.some((c) => c === "this.catalog.search")).toBe(false);
        // The resolved names are exactly the symbols the same run emitted, so the ingester can join.
        const service = entries.find((e) => e.path === "src/app/catalog.service.ts")!;
        expect(service.symbols.map((s) => s.fq_name)).toContain("src.app.catalog.service.CatalogService.search");
      },
    );
  });

  it("resolves calls from arrow-function constants and interface members", () => {
    withProject(
      {
        "src/api.ts": "export interface Api {\n  fetchAll(): string[];\n}\n",
        "src/use.ts":
          "import type { Api } from './api';\nexport const load = (api: Api): string[] => api.fetchAll();\n",
      },
      (entries) => {
        const callees = calls(entries, "src/use.ts", "src.use.load");
        expect(callees).toContain("src.api.Api.fetchAll");
      },
    );
  });
});
