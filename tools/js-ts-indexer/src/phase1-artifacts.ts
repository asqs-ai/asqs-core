/**
 * Phase 1 optional artifacts when `--output <dir>` is set:
 * - packages.jsonl — one JSON object per line per workspace package
 * - index-summary.json — repo layout, tsconfigs, per-package source file lists
 */

import * as fs from "fs";
import * as path from "path";
import type { PackageInfo, ProjectDiscovery } from "./discovery";
import { getSourceFileList } from "./file-list";
import { resolvePackageForSourceFile } from "./package-resolve";
import { toPosix } from "./workspace-packages";

/** Longest matching package `relativeDir` wins; else root `"."`. */
export function owningPackageRelativeDir(
  fileRelPosix: string,
  packages: { relativeDir: string }[],
): string {
  const pkg = resolvePackageForSourceFile(fileRelPosix, packages as PackageInfo[]);
  return pkg?.relativeDir ?? ".";
}

export function writePhase1Artifacts(
  outputDir: string,
  repoRoot: string,
  discovery: ProjectDiscovery,
  indexedFileCount: number,
): void {
  const absOut = path.resolve(outputDir);
  fs.mkdirSync(absOut, { recursive: true });

  const absRepo = path.resolve(repoRoot);
  const allSourceFiles = getSourceFileList(absRepo).map((f) => toPosix(f));

  const filesByPackage = new Map<string, string[]>();
  for (const pkg of discovery.packages) {
    filesByPackage.set(pkg.relativeDir, []);
  }
  for (const f of allSourceFiles) {
    const rd = owningPackageRelativeDir(f, discovery.packages);
    const list = filesByPackage.get(rd);
    if (list) list.push(f);
    else {
      const rootList = filesByPackage.get(".");
      if (rootList) rootList.push(f);
    }
  }

  const packagesJsonlPath = path.join(absOut, "packages.jsonl");
  const jsonlBody = discovery.packages
    .map((pkg) =>
      JSON.stringify({
        name: pkg.name,
        rootDir: pkg.rootDir,
        relativeDir: pkg.relativeDir,
        packageJsonPath: pkg.packageJsonPath,
        isWorkspaceRoot: pkg.isWorkspaceRoot,
        moduleKind: pkg.moduleKind,
        packageRole: pkg.packageRole,
        private: pkg.private,
        hasTypescript: pkg.hasTypescript,
        sourceRoots: pkg.sourceRoots,
        testRoots: pkg.testRoots,
        workspaces: pkg.workspaces,
        dependencies: pkg.dependencies,
        devDependencies: pkg.devDependencies,
        scripts: pkg.scripts,
        exportEntries: pkg.exportEntries,
      }),
    )
    .join("\n");
  fs.writeFileSync(packagesJsonlPath, jsonlBody ? jsonlBody + "\n" : "", "utf-8");

  const summary = {
    schemaVersion: 1,
    repoRoot: absRepo,
    packageManager: discovery.packageManager,
    frameworkSignals: discovery.frameworkSignals,
    testFramework: discovery.testFramework,
    rootScripts: discovery.scripts,
    indexedFileCount,
    nuxtPagePaths: discovery.nuxtPagePaths ?? [],
    tsconfigs: discovery.tsconfigs,
    angularProjects: discovery.angularProjects ?? [],
    packages: discovery.packages.map((pkg) => ({
      name: pkg.name,
      relativeDir: pkg.relativeDir,
      moduleKind: pkg.moduleKind,
      packageRole: pkg.packageRole,
      sourceRoots: pkg.sourceRoots,
      testRoots: pkg.testRoots,
      sourceFiles: filesByPackage.get(pkg.relativeDir) ?? [],
      sourceFileCount: (filesByPackage.get(pkg.relativeDir) ?? []).length,
    })),
  };

  fs.writeFileSync(path.join(absOut, "index-summary.json"), JSON.stringify(summary, null, 2), "utf-8");
}
