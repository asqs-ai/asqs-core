import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import {
  collectWorkspacePackageJsonPaths,
  expandWorkspacePattern,
  readPnpmWorkspacePatterns,
} from "./workspace-packages";

describe("expandWorkspacePattern", () => {
  it("resolves packages/* to child package.json files", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "ws-"));
    fs.mkdirSync(path.join(root, "packages", "a"), { recursive: true });
    fs.mkdirSync(path.join(root, "packages", "b"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "packages", "a", "package.json"),
      JSON.stringify({ name: "a" }),
    );
    fs.writeFileSync(
      path.join(root, "packages", "b", "package.json"),
      JSON.stringify({ name: "b" }),
    );
    const paths = expandWorkspacePattern(root, "packages/*").sort();
    expect(paths.length).toBe(2);
    expect(paths.every((p) => p.endsWith(`${path.sep}package.json`))).toBe(true);
  });

  it("resolves a single package folder", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "ws2-"));
    fs.mkdirSync(path.join(root, "apps", "web"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "apps", "web", "package.json"),
      JSON.stringify({ name: "web" }),
    );
    const paths = expandWorkspacePattern(root, "apps/web");
    expect(paths).toHaveLength(1);
  });

  it("expands packages/** to nested package.json files", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "ws-deep-"));
    fs.mkdirSync(path.join(root, "packages", "a", "nested"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "packages", "a", "package.json"),
      JSON.stringify({ name: "a" }),
    );
    fs.writeFileSync(
      path.join(root, "packages", "a", "nested", "package.json"),
      JSON.stringify({ name: "nested" }),
    );
    const shallow = expandWorkspacePattern(root, "packages/*");
    expect(shallow).toHaveLength(1);
    const deep = expandWorkspacePattern(root, "packages/**").sort();
    expect(deep).toHaveLength(2);
  });
});

describe("readPnpmWorkspacePatterns", () => {
  it("parses packages array", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "pnpm-"));
    fs.writeFileSync(
      path.join(root, "pnpm-workspace.yaml"),
      "packages:\n  - 'packages/*'\n  - \"apps/*\"\n",
    );
    expect(readPnpmWorkspacePatterns(root).sort()).toEqual(["apps/*", "packages/*"]);
  });
});

describe("collectWorkspacePackageJsonPaths", () => {
  it("discovers a member package from npm workspaces", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "merge-"));
    fs.mkdirSync(path.join(root, "packages", "lib"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "package.json"),
      JSON.stringify({ name: "root", workspaces: ["packages/*"] }),
    );
    fs.writeFileSync(
      path.join(root, "packages", "lib", "package.json"),
      JSON.stringify({ name: "lib" }),
    );
    const paths = collectWorkspacePackageJsonPaths(root, ["packages/*"]);
    expect(paths).toHaveLength(1);
    expect(paths[0]).toContain("packages");
  });
});
