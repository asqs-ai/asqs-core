import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { discoverProject } from "./discovery";
import { indexProjectStreaming, type LangIndexerJSON } from "./language-indexer";

describe("indexProjectStreaming DI payload", () => {
  it("emits di_* payload from extractor-native DI edges", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "jst-di-"));
    try {
      fs.writeFileSync(
        path.join(dir, "package.json"),
        JSON.stringify({ name: "di-test", private: true, dependencies: { "@nestjs/common": "^10.0.0" } }),
      );
      fs.writeFileSync(path.join(dir, "tsconfig.json"), JSON.stringify({ compilerOptions: { experimentalDecorators: true } }));
      fs.mkdirSync(path.join(dir, "src"), { recursive: true });
      fs.writeFileSync(
        path.join(dir, "src", "cats.service.ts"),
        [
          "import { Injectable } from '@nestjs/common';",
          "import { CatsRepo } from './cats.repo';",
          "",
          "@Injectable()",
          "export class CatsService {",
          "  constructor(private readonly repo: CatsRepo) {}",
          "}",
          "",
        ].join("\n"),
      );
      fs.writeFileSync(path.join(dir, "src", "cats.repo.ts"), "export class CatsRepo {}\n");

      const discovery = discoverProject(dir);
      const entries: LangIndexerJSON[] = [];
      indexProjectStreaming(dir, discovery, { frameworks: "nest" }, (e) => entries.push(e));

      const svc = entries.find((e) => e.path === "src/cats.service.ts");
      expect(svc).toBeDefined();
      expect(svc!.di_injected_types?.some((e) => e.edge_type === "INJECTS" && e.callee_fq_name === "CatsRepo")).toBe(
        true,
      );
      expect(svc!.di_registered_services).toBeUndefined();
      expect(svc!.di_implements_services).toBeUndefined();
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
});
