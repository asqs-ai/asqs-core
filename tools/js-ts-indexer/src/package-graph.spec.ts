import { describe, expect, it } from "vitest";
import { buildPackageGraphLines } from "./package-graph";
import type { ProjectDiscovery } from "./discovery";

describe("buildPackageGraphLines", () => {
  it("includes PACKAGE_EXPORT edges from exportEntries", () => {
    const discovery: ProjectDiscovery = {
      repoRoot: "/r",
      packages: [
        {
          name: "lib",
          rootDir: "/r",
          packageJsonPath: "/r/package.json",
          relativeDir: ".",
          hasTypescript: true,
          isWorkspaceRoot: true,
          dependencies: [],
          devDependencies: [],
          moduleKind: "esm",
          packageRole: "library",
          sourceRoots: ["."],
          testRoots: [],
          scripts: {},
          exportEntries: [
            { subpath: ".", relPath: "dist/index.js" },
            { subpath: "./core", relPath: "dist/core.js" },
          ],
        },
      ],
      tsconfigs: [],
      angularProjects: [],
      frameworkSignals: {
        nest: false,
        react: false,
        angular: false,
        vue: false,
        solid: false,
        angularjs: false,
        tanstackRouter: false,
        nuxt: false,
      },
      nuxtPagePaths: [],
      testFramework: "",
      scripts: {},
      packageManager: "npm",
    };
    const lines = buildPackageGraphLines(discovery);
    expect(lines.length).toBe(1);
    const exp = lines[0].edges.filter((e) => e.edge_type === "PACKAGE_EXPORT");
    expect(exp.length).toBe(2);
    expect(exp.map((e) => e.callee_fq_name).sort()).toEqual(["dist/core.js", "dist/index.js"]);
    const idx = exp.find((e) => e.callee_fq_name === "dist/index.js");
    expect(idx?.signature).toEqual({ subpaths: ["."] });
    const core = exp.find((e) => e.callee_fq_name === "dist/core.js");
    expect(core?.signature).toEqual({ subpaths: ["./core"] });
  });

  it("merges PACKAGE_EXPORT signatures when multiple condition paths hit the same file", () => {
    const discovery: ProjectDiscovery = {
      repoRoot: "/r",
      packages: [
        {
          name: "lib",
          rootDir: "/r",
          packageJsonPath: "/r/package.json",
          relativeDir: ".",
          hasTypescript: true,
          isWorkspaceRoot: true,
          dependencies: [],
          devDependencies: [],
          moduleKind: "dual",
          packageRole: "library",
          sourceRoots: ["."],
          testRoots: [],
          scripts: {},
          exportEntries: [
            { subpath: ".", relPath: "dist/x.js", conditions: "import" },
            { subpath: ".", relPath: "dist/x.js", conditions: "require" },
            { subpath: "./lite", relPath: "dist/x.js", conditions: "default" },
          ],
        },
      ],
      tsconfigs: [],
      angularProjects: [],
      frameworkSignals: {
        nest: false,
        react: false,
        angular: false,
        vue: false,
        solid: false,
        angularjs: false,
        tanstackRouter: false,
        nuxt: false,
      },
      nuxtPagePaths: [],
      testFramework: "",
      scripts: {},
      packageManager: "npm",
    };
    const lines = buildPackageGraphLines(discovery);
    const exp = lines[0].edges.filter((e) => e.edge_type === "PACKAGE_EXPORT");
    expect(exp.length).toBe(1);
    expect(exp[0].signature).toEqual({
      subpaths: [".", "./lite"].sort(),
      export_conditions: ["default", "import", "require"],
    });
  });
});
