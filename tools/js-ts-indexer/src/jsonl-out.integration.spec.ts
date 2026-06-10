import { spawnSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";

const indexerJs = path.join(process.cwd(), "dist", "index.js");
const hasIndexer = fs.existsSync(indexerJs);

describe("CLI --jsonl-out", () => {
  (hasIndexer ? it : it.skip)("writes the same logical JSONL as would go to stdout", () => {
    const repo = fs.mkdtempSync(path.join(os.tmpdir(), "jst-jsonl-"));
    fs.writeFileSync(path.join(repo, "package.json"), JSON.stringify({ name: "solo-jsonl" }));
    fs.mkdirSync(path.join(repo, "src"), { recursive: true });
    fs.writeFileSync(path.join(repo, "src", "m.ts"), "export const m = 2;\n");

    const outFile = path.join(repo, "out.jsonl");

    const rFile = spawnSync(process.execPath, [indexerJs, "--repo", repo, "--jsonl-out", outFile], {
      encoding: "utf-8",
      env: { ...process.env },
    });
    expect(rFile.status, rFile.stderr).toBe(0);
    const fileBody = fs.readFileSync(outFile, "utf-8").trim();
    const fileLines = fileBody.split("\n").filter(Boolean);
    expect(fileLines.length).toBeGreaterThan(0);

    const rStdout = spawnSync(process.execPath, [indexerJs, "--repo", repo], {
      encoding: "utf-8",
      env: { ...process.env },
    });
    expect(rStdout.status, rStdout.stderr).toBe(0);
    const stdoutLines = rStdout.stdout
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
    expect(stdoutLines.length).toBe(fileLines.length);

    const filePaths = fileLines.map((ln) => {
      const o = JSON.parse(ln) as { path: string };
      return o.path;
    });
    const stdoutPaths = stdoutLines.map((ln) => {
      const o = JSON.parse(ln) as { path: string };
      return o.path;
    });
    expect(new Set(filePaths)).toEqual(new Set(stdoutPaths));
  });
});
