import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { discoverProject } from "./discovery";
import { indexProjectStreaming, type LangIndexerJSON } from "./language-indexer";

describe("indexProjectStreaming IMPORTS edges", () => {
  it("resolves local imports to callee MODULE fq_name (not ./specifier)", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "jst-imp-"));
    try {
      fs.writeFileSync(
        path.join(dir, "tsconfig.json"),
        JSON.stringify({
          compilerOptions: {
            target: "ES2020",
            module: "ESNext",
            moduleResolution: "node",
            strict: true,
          },
          include: ["*.ts"],
        }),
      );
      fs.writeFileSync(path.join(dir, "package.json"), JSON.stringify({ name: "t", private: true }));
      fs.writeFileSync(path.join(dir, "dep.ts"), "export const x = 1;\n");
      fs.writeFileSync(path.join(dir, "main.ts"), "import { x } from './dep';\nconsole.log(x);\n");

      const discovery = discoverProject(dir);
      const entries: LangIndexerJSON[] = [];
      indexProjectStreaming(dir, discovery, { frameworks: "none" }, (e) => entries.push(e));

      const main = entries.find((e) => e.path === "main.ts");
      expect(main).toBeDefined();
      const imports = main!.edges.filter((e) => e.edge_type.toUpperCase() === "IMPORTS");
      expect(imports.length).toBeGreaterThanOrEqual(1);
      expect(imports.some((e) => e.callee_fq_name === "dep")).toBe(true);
      expect(imports.every((e) => !e.callee_fq_name.startsWith(".") && !e.callee_fq_name.startsWith("/"))).toBe(
        true,
      );
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
});
