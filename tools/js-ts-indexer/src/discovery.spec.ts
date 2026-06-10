import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { collectExportFileTargets, discoverProject, parsePackageExports } from "./discovery";
import { owningPackageRelativeDir } from "./phase1-artifacts";

describe("discoverProject", () => {
  it("discovers root + workspace packages with module kind and split deps", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "disc-"));
    fs.mkdirSync(path.join(root, "packages", "api"), { recursive: true });
    fs.writeFileSync(
      path.join(root, "package.json"),
      JSON.stringify({
        name: "mono",
        private: true,
        workspaces: ["packages/*"],
        dependencies: { react: "^18.0.0" },
        devDependencies: { vitest: "^1.0.0" },
      }),
    );
    fs.writeFileSync(
      path.join(root, "packages", "api", "package.json"),
      JSON.stringify({
        name: "@mono/api",
        private: true,
        type: "module",
        dependencies: { "@nestjs/common": "^10.0.0" },
        devDependencies: { typescript: "^5.0.0" },
      }),
    );
    const d = discoverProject(root);
    expect(d.packages.length).toBe(2);
    const api = d.packages.find((p) => p.name === "@mono/api");
    expect(api).toBeDefined();
    expect(api!.moduleKind).toBe("esm");
    expect(api!.dependencies).toContain("@nestjs/common");
    expect(api!.devDependencies).toContain("typescript");
    expect(api!.packageRole).toBe("library");
    expect(d.frameworkSignals.react).toBe(true);
    expect(d.frameworkSignals.nest).toBe(true);
  });

  it("sets react signal when only react-router-dom is listed (no direct react dependency key)", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "rr-"));
    fs.writeFileSync(
      path.join(root, "package.json"),
      JSON.stringify({
        name: "spa",
        dependencies: { "react-router-dom": "^6.0.0" },
      }),
    );
    const d = discoverProject(root);
    expect(d.frameworkSignals.react).toBe(true);
  });

  it("resolves mainEntry and binEntries for local paths", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "pkg-"));
    fs.mkdirSync(path.join(root, "src"), { recursive: true });
    fs.mkdirSync(path.join(root, "bin"), { recursive: true });
    fs.writeFileSync(path.join(root, "src", "index.ts"), "export {}\n");
    fs.writeFileSync(path.join(root, "bin", "run.ts"), "export {}\n");
    fs.writeFileSync(
      path.join(root, "package.json"),
      JSON.stringify({
        name: "cli-pkg",
        main: "./src/index.ts",
        bin: { "my-cli": "./bin/run.ts" },
      }),
    );
    const d = discoverProject(root);
    const pkg = d.packages.find((p) => p.name === "cli-pkg");
    expect(pkg?.mainEntry).toBe("src/index.ts");
    expect(pkg?.binEntries).toEqual([{ command: "my-cli", relPath: "bin/run.ts" }]);
  });

  it("records tsconfig extends and references", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "ts-"));
    fs.writeFileSync(path.join(root, "package.json"), JSON.stringify({ name: "x" }));
    fs.writeFileSync(
      path.join(root, "tsconfig.json"),
      JSON.stringify({
        extends: "./tsconfig.base.json",
        references: [{ path: "./other" }],
        include: ["src"],
      }),
    );
    const d = discoverProject(root);
    const main = d.tsconfigs.find((t) => t.path.endsWith("tsconfig.json"));
    expect(main?.extends).toBe("./tsconfig.base.json");
    expect(main?.references).toEqual(["./other"]);
    expect(main?.include).toEqual(["src"]);
  });

  it("collects nuxt page paths when nuxt dependency is present", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "nuxt-"));
    fs.mkdirSync(path.join(root, "pages", "admin"), { recursive: true });
    fs.writeFileSync(path.join(root, "pages", "index.vue"), "<template></template>\n");
    fs.writeFileSync(path.join(root, "pages", "admin", "settings.vue"), "<template></template>\n");
    fs.writeFileSync(
      path.join(root, "package.json"),
      JSON.stringify({ name: "nuxt-app", dependencies: { nuxt: "^3.0.0" } }),
    );
    const d = discoverProject(root);
    expect(d.frameworkSignals.nuxt).toBe(true);
    expect(d.nuxtPagePaths.sort()).toEqual(["pages/admin/settings.vue", "pages/index.vue"]);
  });

  it("merges compilerOptions along extends chain (base → leaf)", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "tsm-"));
    fs.writeFileSync(path.join(root, "package.json"), JSON.stringify({ name: "m" }));
    fs.writeFileSync(
      path.join(root, "tsconfig.base.json"),
      JSON.stringify({
        compilerOptions: { target: "ES2017", strict: true, module: "CommonJS" },
      }),
    );
    fs.writeFileSync(
      path.join(root, "tsconfig.json"),
      JSON.stringify({
        extends: "./tsconfig.base.json",
        compilerOptions: { target: "ES2020", moduleResolution: "node" },
      }),
    );
    const d = discoverProject(root);
    const main = d.tsconfigs.find((t) => t.path.endsWith("tsconfig.json"));
    expect(main?.extendsChain?.length).toBeGreaterThanOrEqual(2);
    expect(main?.mergedCompilerOptions?.target).toBe("ES2020");
    expect(main?.mergedCompilerOptions?.strict).toBe(true);
    expect(main?.mergedCompilerOptions?.module).toBe("CommonJS");
    expect(main?.mergedCompilerOptions?.moduleResolution).toBe("node");
  });
});

describe("parsePackageExports", () => {
  it("resolves string and object exports to files under the package", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "exp-"));
    fs.mkdirSync(path.join(root, "dist"), { recursive: true });
    fs.writeFileSync(path.join(root, "dist", "index.js"), "module.exports=1\n");
    const pkgRoot = root;
    expect(parsePackageExports(root, pkgRoot, "./dist/index.js")).toEqual([
      { subpath: ".", relPath: "dist/index.js" },
    ]);
    const entries = parsePackageExports(root, pkgRoot, {
      ".": "./dist/index.js",
      "./lite": "./dist/index.js",
    });
    expect(entries.length).toBe(2);
    expect(entries.every((e) => e.relPath === "dist/index.js")).toBe(true);
  });

  it("emits multiple PACKAGE_EXPORT targets for import+require+default conditions", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "exp2-"));
    fs.mkdirSync(path.join(root, "dist"), { recursive: true });
    fs.writeFileSync(path.join(root, "dist", "index.js"), "x");
    fs.writeFileSync(path.join(root, "dist", "index.cjs"), "x");
    fs.writeFileSync(path.join(root, "dist", "index.mjs"), "x");
    const entries = parsePackageExports(root, root, {
      ".": {
        import: "./dist/index.mjs",
        require: "./dist/index.cjs",
        default: "./dist/index.js",
      },
    });
    const rels = entries.map((e) => e.relPath).sort();
    expect(rels).toEqual(["dist/index.cjs", "dist/index.js", "dist/index.mjs"]);
    const byRel = Object.fromEntries(entries.map((e) => [e.relPath, e.conditions]));
    expect(byRel["dist/index.mjs"]).toBe("import");
    expect(byRel["dist/index.cjs"]).toBe("require");
    expect(byRel["dist/index.js"]).toBe("default");
  });

  it("resolves nested node import/require leaves separately", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "exp3-"));
    fs.mkdirSync(path.join(root, "dist"), { recursive: true });
    fs.writeFileSync(path.join(root, "dist", "n.mjs"), "x");
    fs.writeFileSync(path.join(root, "dist", "n.cjs"), "x");
    fs.writeFileSync(path.join(root, "dist", "fallback.js"), "x");
    const entries = parsePackageExports(root, root, {
      ".": {
        node: {
          import: "./dist/n.mjs",
          require: "./dist/n.cjs",
        },
        default: "./dist/fallback.js",
      },
    });
    expect(entries).toHaveLength(3);
    expect(entries.find((e) => e.relPath === "dist/n.mjs")?.conditions).toBe("node>import");
    expect(entries.find((e) => e.relPath === "dist/n.cjs")?.conditions).toBe("node>require");
    expect(entries.find((e) => e.relPath === "dist/fallback.js")?.conditions).toBe("default");
  });
});

describe("collectExportFileTargets", () => {
  it("gathers nested condition strings", () => {
    const t = collectExportFileTargets({
      import: "./a.js",
      require: "./b.cjs",
      nested: { default: "./c.js" },
    });
    expect(t.sort()).toEqual(["./a.js", "./b.cjs", "./c.js"]);
  });
});

describe("owningPackageRelativeDir", () => {
  it("prefers longest package prefix", () => {
    const pkgs = [{ relativeDir: "." }, { relativeDir: "packages/a" }, { relativeDir: "packages" }];
    expect(owningPackageRelativeDir("packages/a/src/x.ts", pkgs)).toBe("packages/a");
    expect(owningPackageRelativeDir("packages/root-only.ts", pkgs)).toBe("packages");
    expect(owningPackageRelativeDir("root.ts", pkgs)).toBe(".");
  });
});
