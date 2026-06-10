import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { discoverProject } from "./discovery";
import { writePhase1Artifacts } from "./phase1-artifacts";

describe("writePhase1Artifacts", () => {
  it("writes packages.jsonl and index-summary.json", () => {
    const repo = fs.mkdtempSync(path.join(os.tmpdir(), "art-"));
    fs.writeFileSync(path.join(repo, "package.json"), JSON.stringify({ name: "solo" }));
    fs.mkdirSync(path.join(repo, "src"), { recursive: true });
    fs.writeFileSync(path.join(repo, "src", "index.ts"), "export {};\n");

    const out = fs.mkdtempSync(path.join(os.tmpdir(), "out-"));
    const discovery = discoverProject(repo);
    writePhase1Artifacts(out, repo, discovery, 1);

    const jsonl = fs.readFileSync(path.join(out, "packages.jsonl"), "utf-8").trim();
    const row = JSON.parse(jsonl.split("\n")[0]) as { name: string; moduleKind: string };
    expect(row.name).toBe("solo");
    expect(row.moduleKind).toBeDefined();

    const summary = JSON.parse(fs.readFileSync(path.join(out, "index-summary.json"), "utf-8")) as {
      indexedFileCount: number;
      packages: { sourceFiles: string[] }[];
    };
    expect(summary.indexedFileCount).toBe(1);
    expect(summary.packages[0].sourceFiles.some((f) => f.includes("src/index.ts"))).toBe(true);
  });
});
