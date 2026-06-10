/**
 * Layer A — Project discovery: workspace packages, package.json, tsconfig graph hints,
 * Angular workspace projects, framework signals, module kind, source/test roots.
 */

import * as fs from "fs";
import * as path from "path";
import { resolveTsConfigExtendsChainAndMerge } from "./tsconfig-merge";
import {
  collectWorkspacePackageJsonPaths,
  normalizeWorkspacesField,
  toPosix,
} from "./workspace-packages";
import {
  enumerateConditionalExportLeaves,
  formatExportConditionChain,
} from "./package-exports-resolve";

export type ModuleKind = "esm" | "commonjs" | "dual" | "unknown";
export type PackageRole = "application" | "library" | "cli" | "unknown";

export interface PackageInfo {
  name: string;
  rootDir: string;
  packageJsonPath: string;
  /** POSIX path relative to repo root: `"."` or `packages/foo`. */
  relativeDir: string;
  hasTypescript: boolean;
  isWorkspaceRoot: boolean;
  /** Only set on the workspace root package. */
  workspaces?: string[];
  /** Production dependency names. */
  dependencies: string[];
  /** Dev dependency names. */
  devDependencies: string[];
  moduleKind: ModuleKind;
  packageRole: PackageRole;
  /** Source roots relative to repo root (posix). */
  sourceRoots: string[];
  /** Test roots relative to repo root (posix). */
  testRoots: string[];
  scripts: Record<string, string>;
  private?: boolean;
  /** Resolved repo-relative POSIX path for `main` when it points at a local file. */
  mainEntry?: string;
  /** Resolved repo-relative POSIX path for `module` when set and local. */
  moduleEntry?: string;
  /** `index.*` at package root when `main`/`module` are absent or unresolved. */
  packageDefaultEntry?: string;
  /** `bin` field: command name → entry source file (repo-relative POSIX). */
  binEntries?: { command: string; relPath: string }[];
  /**
   * Resolved `exports` subpath → entry file (repo-relative POSIX), with optional Node conditional-export
   * chain per leaf (e.g. `node>import`).
   */
  exportEntries?: PackageExportEntry[];
}

/** One resolved leaf of `package.json` `"exports"` (Node conditional exports). */
export type PackageExportEntry = {
  subpath: string;
  relPath: string;
  conditions?: string;
};

export interface TsConfigInfo {
  path: string;
  /** Nearest package folder relative to repo (`"."` for root). */
  packageRelativeDir: string;
  rootDir?: string;
  include?: string[];
  exclude?: string[];
  extends?: string;
  /** Resolved `extends` chain, base → leaf (repo-relative POSIX). Same order as shallow `compilerOptions` merge. */
  extendsChain?: string[];
  /** Shallow merge of `compilerOptions` along `extendsChain` (later configs override earlier). */
  mergedCompilerOptions?: Record<string, unknown>;
  references?: string[];
}

export interface AngularWorkspaceProject {
  name: string;
  /** Project root relative to repo (posix), e.g. `projects/my-app` or empty. */
  root: string;
  projectType: string;
  sourceRoot?: string;
}

export interface ProjectDiscovery {
  repoRoot: string;
  packages: PackageInfo[];
  tsconfigs: TsConfigInfo[];
  angularProjects: AngularWorkspaceProject[];
  frameworkSignals: {
    nest: boolean;
    react: boolean;
    angular: boolean;
    vue: boolean;
    solid: boolean;
    angularjs: boolean;
    /** `@tanstack/react-router` in any workspace package. */
    tanstackRouter: boolean;
    /** `nuxt` / `@nuxt/schema` in any workspace package. */
    nuxt: boolean;
  };
  /** Repo-relative POSIX paths under `pages/` when Nuxt is detected (file-based routes hint). */
  nuxtPagePaths: string[];
  /** From the workspace root package.json only (runner / evaluation). */
  testFramework: string;
  scripts: Record<string, string>;
  packageManager: "npm" | "yarn" | "pnpm";
}

/** Runtime/framework label for evaluation: nest, react, angular, vue, solid, angularjs, or node (plain Node.js). */
export function runtimeLabel(
  discovery: ProjectDiscovery,
): "nest" | "react" | "angular" | "vue" | "solid" | "angularjs" | "node" {
  if (discovery.frameworkSignals.nest) return "nest";
  if (discovery.frameworkSignals.react) return "react";
  if (discovery.frameworkSignals.angular) return "angular";
  if (discovery.frameworkSignals.vue) return "vue";
  if (discovery.frameworkSignals.solid) return "solid";
  if (discovery.frameworkSignals.angularjs) return "angularjs";
  return "node";
}

function readJson<T>(filePath: string): T | null {
  try {
    const raw = fs.readFileSync(filePath, "utf-8");
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

function pathExists(p: string): boolean {
  try {
    fs.accessSync(p);
    return true;
  } catch {
    return false;
  }
}

function extractScripts(raw: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (raw && typeof raw === "object") {
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof v === "string") out[k] = v;
    }
  }
  return out;
}

function parseModuleKind(pkg: { type?: string; exports?: unknown }): ModuleKind {
  const t = pkg.type;
  const exp = pkg.exports;
  const dualish = exp != null && typeof exp === "object" && looksLikeDualExports(exp);

  if (t === "module") {
    return dualish ? "dual" : "esm";
  }
  if (t === "commonjs") {
    return dualish ? "dual" : "commonjs";
  }
  if (dualish) return "dual";
  if (exp != null) {
    const s = JSON.stringify(exp);
    if (s.includes("import") && s.includes("require")) return "dual";
  }
  return t === undefined || t === "" ? "commonjs" : "unknown";
}

function looksLikeDualExports(exp: object): boolean {
  const s = JSON.stringify(exp);
  return s.includes("import") && s.includes("require");
}

function parseAngularWorkspace(absRoot: string): AngularWorkspaceProject[] {
  const p = path.join(absRoot, "angular.json");
  if (!fs.existsSync(p)) return [];
  const j = readJson<{
    projects?: Record<
      string,
      { projectType?: string; sourceRoot?: string; root?: string } | undefined
    >;
  }>(p);
  if (!j?.projects || typeof j.projects !== "object") return [];
  const out: AngularWorkspaceProject[] = [];
  for (const [name, proj] of Object.entries(j.projects)) {
    if (!proj || typeof proj !== "object") continue;
    const root = typeof proj.root === "string" ? toPosix(proj.root.replace(/^\.\//, "")) : "";
    const projectType = typeof proj.projectType === "string" ? proj.projectType : "unknown";
    const sourceRoot = typeof proj.sourceRoot === "string" ? toPosix(proj.sourceRoot) : undefined;
    out.push({ name, root, projectType, sourceRoot });
  }
  return out;
}

function inferSourceTestRoots(absPackageDir: string, repoRoot: string): { sourceRoots: string[]; testRoots: string[] } {
  const rel = (abs: string): string => {
    const r = path.relative(repoRoot, abs);
    if (r === "") return ".";
    return toPosix(r);
  };
  const sourceRoots: string[] = [];
  const testRoots: string[] = [];
  for (const c of ["src", "lib", "app", "source"]) {
    const d = path.join(absPackageDir, c);
    if (fs.existsSync(d) && fs.statSync(d).isDirectory()) {
      sourceRoots.push(rel(d));
    }
  }
  if (sourceRoots.length === 0) {
    sourceRoots.push(rel(absPackageDir));
  }
  for (const c of ["test", "tests", "__tests__", "e2e", "e2e-tests", "spec", "specs"]) {
    const d = path.join(absPackageDir, c);
    if (fs.existsSync(d) && fs.statSync(d).isDirectory()) {
      testRoots.push(rel(d));
    }
  }
  return { sourceRoots, testRoots };
}

function inferPackageRole(
  pkg: { bin?: unknown; private?: boolean },
  relativeDir: string,
  angularProjects: AngularWorkspaceProject[],
): PackageRole {
  if (pkg.bin && typeof pkg.bin === "object" && Object.keys(pkg.bin as object).length > 0) {
    return "cli";
  }
  if (pkg.bin && typeof pkg.bin === "string" && pkg.bin.length > 0) {
    return "cli";
  }
  for (const ap of angularProjects) {
    const apRoot = (ap.root || "").replace(/^\.\/?/, "").replace(/\/$/, "");
    if (!apRoot) continue;
    if (relativeDir === apRoot || relativeDir.startsWith(apRoot + "/")) {
      if (ap.projectType === "library") return "library";
      if (ap.projectType === "application") return "application";
    }
  }
  if (relativeDir !== "." && relativeDir.split("/").includes("packages")) {
    return pkg.private === true ? "library" : "unknown";
  }
  return "unknown";
}

function mergeDepsIntoFrameworkSignals(
  signals: ProjectDiscovery["frameworkSignals"],
  deps: Record<string, string>,
): void {
  if (deps["@nestjs/core"] || deps["@nestjs/common"]) signals.nest = true;
  // SPA apps often list react-router-dom without a top-level "react" key in this package.json (workspace split);
  // still need React enrichers so PAGE_ROUTE symbols exist for E2E gap listing.
  if (deps["react"]) signals.react = true;
  if (deps["react-router-dom"] || deps["react-router"]) signals.react = true;
  if (deps["@angular/core"]) signals.angular = true;
  if (deps["vue-router"] || deps["@ionic/vue-router"]) signals.vue = true;
  if (deps["@solidjs/router"]) signals.solid = true;
  if (deps["angular"] && deps["angular"].startsWith("1.")) signals.angularjs = true;
  if (deps["@tanstack/react-router"]) signals.tanstackRouter = true;
  if (deps["nuxt"] || deps["@nuxt/schema"]) signals.nuxt = true;
}

/** Nuxt file routes: collect `.vue` files under `pages/` (repo-relative POSIX paths). */
function collectNuxtPagePaths(absRoot: string): string[] {
  const pagesDir = path.join(absRoot, "pages");
  if (!fs.existsSync(pagesDir) || !fs.statSync(pagesDir).isDirectory()) {
    return [];
  }
  const out: string[] = [];
  const walk = (dir: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const ent of entries) {
      const p = path.join(dir, ent.name);
      if (ent.isDirectory()) {
        walk(p);
      } else if (ent.isFile() && ent.name.endsWith(".vue")) {
        const rel = path.relative(absRoot, p);
        out.push(toPosix(rel));
      }
    }
  };
  walk(pagesDir);
  return out.sort();
}

function detectTestFramework(deps: Record<string, string>): string {
  if (deps["jest"]) return "jest";
  if (deps["vitest"]) return "vitest";
  if (deps["jasmine-core"] || deps["jasmine"]) return "jasmine";
  if (deps["mocha"]) return "mocha";
  if (deps["ava"]) return "ava";
  if (deps["@jest/core"]) return "jest";
  if (deps["@vitest/runner"]) return "vitest";
  return "";
}

function loadTsConfigInfo(tsconfigPath: string, repoRoot: string): TsConfigInfo | null {
  const raw = readJson<{
    extends?: string;
    references?: { path?: string }[];
    compilerOptions?: { rootDir?: string };
    include?: string[];
    exclude?: string[];
  }>(tsconfigPath);
  if (!raw) return null;
  const dir = path.dirname(tsconfigPath);
  let pkgRel = path.relative(repoRoot, dir);
  if (pkgRel === "") pkgRel = ".";
  const refs = raw.references?.map((r) => r.path).filter((x): x is string => typeof x === "string");
  const { extendsChain, mergedCompilerOptions } = resolveTsConfigExtendsChainAndMerge(tsconfigPath, repoRoot);
  const merged =
    mergedCompilerOptions && Object.keys(mergedCompilerOptions).length > 0 ? mergedCompilerOptions : undefined;
  const chain = extendsChain.length > 0 ? extendsChain : undefined;
  const mergedRootDir =
    typeof merged?.rootDir === "string" ? merged.rootDir : raw.compilerOptions?.rootDir;
  return {
    path: tsconfigPath,
    packageRelativeDir: toPosix(pkgRel),
    rootDir: mergedRootDir,
    include: raw.include,
    exclude: raw.exclude,
    extends: typeof raw.extends === "string" ? raw.extends : undefined,
    extendsChain: chain,
    mergedCompilerOptions: merged,
    references: refs && refs.length > 0 ? refs : undefined,
  };
}

function collectTsConfigsForRepo(repoRoot: string, packages: PackageInfo[]): TsConfigInfo[] {
  const seen = new Set<string>();
  const out: TsConfigInfo[] = [];
  const tryAdd = (absPath: string) => {
    if (!fs.existsSync(absPath) || seen.has(absPath)) return;
    seen.add(absPath);
    const info = loadTsConfigInfo(absPath, repoRoot);
    if (info) out.push(info);
  };

  for (const name of ["tsconfig.json", "jsconfig.json"]) {
    tryAdd(path.join(repoRoot, name));
  }
  for (const pkg of packages) {
    for (const name of ["tsconfig.json", "jsconfig.json", "tsconfig.build.json", "tsconfig.lib.json"]) {
      tryAdd(path.join(pkg.rootDir, name));
    }
  }
  return out;
}

interface PackageJsonShape {
  name?: string;
  private?: boolean;
  type?: string;
  main?: string;
  module?: string;
  bin?: unknown;
  exports?: unknown;
  workspaces?: unknown;
  dependencies?: Record<string, string>;
  devDependencies?: Record<string, string>;
  scripts?: Record<string, unknown>;
}

/** Resolve `main` / `module` / bin paths that start with `./` or `../` to a repo-relative POSIX file path. */
function resolvePackageEntryPath(repoRoot: string, pkgRoot: string, field?: string): string | undefined {
  if (!field || typeof field !== "string") return undefined;
  const t = field.trim();
  if (!t.startsWith(".")) return undefined;
  let abs = path.resolve(pkgRoot, t);
  if (pathExists(abs) && fs.statSync(abs).isDirectory()) {
    let found = false;
    for (const base of ["index.ts", "index.tsx", "index.mjs", "index.js", "index.cjs"]) {
      const cand = path.join(abs, base);
      if (pathExists(cand) && fs.statSync(cand).isFile()) {
        abs = cand;
        found = true;
        break;
      }
    }
    if (!found) return undefined;
  }
  if ((!pathExists(abs) || !fs.statSync(abs).isFile()) && t.endsWith(".js")) {
    const ts = abs.slice(0, -3) + ".ts";
    if (pathExists(ts) && fs.statSync(ts).isFile()) abs = ts;
  }
  if (!pathExists(abs) || !fs.statSync(abs).isFile()) return undefined;
  const rel = path.relative(repoRoot, abs);
  if (rel.startsWith("..") || path.isAbsolute(rel)) return undefined;
  return toPosix(rel);
}

function defaultIndexEntry(repoRoot: string, pkgRoot: string): string | undefined {
  for (const base of ["index.ts", "index.tsx", "index.mjs", "index.js", "index.cjs"]) {
    const abs = path.join(pkgRoot, base);
    if (pathExists(abs) && fs.statSync(abs).isFile()) {
      const rel = path.relative(repoRoot, abs);
      if (rel.startsWith("..") || path.isAbsolute(rel)) continue;
      return toPosix(rel);
    }
  }
  return undefined;
}

/** Collect all `./…` file targets from one export entry value (respects conditional tree, union of leaves). */
export function collectExportFileTargets(val: unknown): string[] {
  const leaves = enumerateConditionalExportLeaves(val, []);
  const specs = [...new Set(leaves.map((l) => l.specifier))];
  specs.sort();
  return specs;
}

/**
 * Resolve `package.json` `exports` to local files (`./…` targets only), enumerating Node conditional-export
 * leaves (nested `import` / `require` / `node` / `types` / `default` / custom keys) in object key order.
 * Exported for unit tests.
 */
export function parsePackageExports(
  repoRoot: string,
  pkgRoot: string,
  exports: unknown,
): PackageExportEntry[] {
  if (exports == null) {
    return [];
  }
  const out: PackageExportEntry[] = [];
  const seen = new Set<string>();
  if (typeof exports === "string") {
    const t = exports.trim();
    const rel = resolvePackageEntryPath(repoRoot, pkgRoot, t);
    if (rel) {
      out.push({ subpath: ".", relPath: rel });
    }
    return out;
  }
  if (typeof exports !== "object" || Array.isArray(exports)) {
    return [];
  }
  for (const [subpath, val] of Object.entries(exports as Record<string, unknown>)) {
    const leaves = enumerateConditionalExportLeaves(val, []);
    for (const { specifier, conditions } of leaves) {
      const rel = resolvePackageEntryPath(repoRoot, pkgRoot, specifier);
      if (!rel) {
        continue;
      }
      const condStr = formatExportConditionChain(conditions);
      const key = `${subpath}::${rel}::${condStr ?? ""}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      const entry: PackageExportEntry = { subpath: subpath || ".", relPath: rel };
      if (condStr) {
        entry.conditions = condStr;
      }
      out.push(entry);
    }
  }
  return out;
}

function parseBinEntries(
  repoRoot: string,
  pkgRoot: string,
  pkgName: string,
  bin: unknown,
): { command: string; relPath: string }[] {
  if (!bin) return [];
  if (typeof bin === "string") {
    const rel = resolvePackageEntryPath(repoRoot, pkgRoot, bin);
    return rel ? [{ command: pkgName, relPath: rel }] : [];
  }
  if (typeof bin === "object" && bin !== null && !Array.isArray(bin)) {
    const out: { command: string; relPath: string }[] = [];
    for (const [cmd, val] of Object.entries(bin as Record<string, unknown>)) {
      if (typeof val === "string") {
        const rel = resolvePackageEntryPath(repoRoot, pkgRoot, val);
        if (rel) out.push({ command: cmd, relPath: rel });
      }
    }
    return out;
  }
  return [];
}

function loadPackageInfo(
  packageJsonPath: string,
  repoRoot: string,
  isWorkspaceRoot: boolean,
  angularProjects: AngularWorkspaceProject[],
): PackageInfo | null {
  const data = readJson<PackageJsonShape>(packageJsonPath);
  if (!data) return null;
  const rootDir = path.dirname(packageJsonPath);
  let relativeDir = path.relative(repoRoot, rootDir);
  if (relativeDir === "") relativeDir = ".";
  relativeDir = toPosix(relativeDir);

  const deps = data.dependencies ?? {};
  const devDeps = data.devDependencies ?? {};

  const { sourceRoots, testRoots } = inferSourceTestRoots(rootDir, repoRoot);
  const moduleKind = parseModuleKind(data);
  const packageRole = inferPackageRole(data, relativeDir, angularProjects);

  const mainEntry = resolvePackageEntryPath(repoRoot, rootDir, data.main);
  const moduleEntry = resolvePackageEntryPath(repoRoot, rootDir, data.module);
  const packageDefaultEntry =
    mainEntry || moduleEntry ? undefined : defaultIndexEntry(repoRoot, rootDir);
  const binEntries = parseBinEntries(
    repoRoot,
    rootDir,
    data.name ?? path.basename(rootDir),
    data.bin,
  );
  const exportEntries = parsePackageExports(repoRoot, rootDir, data.exports);

  return {
    name: data.name ?? path.basename(rootDir),
    rootDir,
    packageJsonPath,
    relativeDir,
    hasTypescript: fs.existsSync(path.join(rootDir, "tsconfig.json")),
    isWorkspaceRoot,
    workspaces: isWorkspaceRoot ? normalizeWorkspacesField(data.workspaces) : undefined,
    dependencies: Object.keys(deps).sort(),
    devDependencies: Object.keys(devDeps).sort(),
    moduleKind,
    packageRole,
    sourceRoots,
    testRoots,
    scripts: extractScripts(data.scripts),
    private: typeof data.private === "boolean" ? data.private : undefined,
    mainEntry,
    moduleEntry,
    packageDefaultEntry,
    binEntries: binEntries.length > 0 ? binEntries : undefined,
    exportEntries: exportEntries.length > 0 ? exportEntries : undefined,
  };
}

export function discoverProject(repoRoot: string): ProjectDiscovery {
  const absRoot = path.resolve(repoRoot);
  const rootPkgPath = path.join(absRoot, "package.json");
  const rootPkg = readJson<PackageJsonShape>(rootPkgPath);

  let packageManager: "npm" | "yarn" | "pnpm" = "npm";
  if (pathExists(path.join(absRoot, "yarn.lock"))) {
    packageManager = "yarn";
  } else if (pathExists(path.join(absRoot, "pnpm-lock.yaml"))) {
    packageManager = "pnpm";
  }

  const angularProjects = parseAngularWorkspace(absRoot);
  const frameworkSignals: ProjectDiscovery["frameworkSignals"] = {
    nest: false,
    react: false,
    angular: false,
    vue: false,
    solid: false,
    angularjs: false,
    tanstackRouter: false,
    nuxt: false,
  };

  if (fs.existsSync(path.join(absRoot, "angular.json"))) {
    frameworkSignals.angular = true;
  }

  const memberJsonPaths = rootPkg
    ? collectWorkspacePackageJsonPaths(absRoot, rootPkg.workspaces)
    : [];
  const rootResolved = path.resolve(rootPkgPath);

  const uniquePaths = new Set<string>([rootResolved]);
  for (const p of memberJsonPaths) {
    if (path.resolve(p) !== rootResolved) {
      uniquePaths.add(path.resolve(p));
    }
  }

  const packages: PackageInfo[] = [];
  for (const pj of uniquePaths) {
    const isRoot = path.resolve(pj) === rootResolved;
    const info = loadPackageInfo(pj, absRoot, isRoot, angularProjects);
    if (!info) continue;
    const rawPkg = readJson<PackageJsonShape>(pj);
    mergeDepsIntoFrameworkSignals(frameworkSignals, {
      ...(rawPkg?.dependencies ?? {}),
      ...(rawPkg?.devDependencies ?? {}),
    });
    packages.push(info);
  }

  packages.sort((a, b) => {
    if (a.relativeDir === ".") return -1;
    if (b.relativeDir === ".") return 1;
    return a.relativeDir.localeCompare(b.relativeDir);
  });

  let testFramework = "";
  const scripts: Record<string, string> = {};
  if (rootPkg) {
    testFramework = detectTestFramework({
      ...(rootPkg.dependencies ?? {}),
      ...(rootPkg.devDependencies ?? {}),
    });
    Object.assign(scripts, extractScripts(rootPkg.scripts));
  }

  const tsconfigs = collectTsConfigsForRepo(absRoot, packages);

  const nuxtPagePaths = frameworkSignals.nuxt ? collectNuxtPagePaths(absRoot) : [];

  return {
    repoRoot: absRoot,
    packages,
    tsconfigs,
    angularProjects,
    frameworkSignals,
    nuxtPagePaths,
    testFramework,
    scripts,
    packageManager,
  };
}
