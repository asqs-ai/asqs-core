/**
 * Map a repo-relative source path to the workspace package that owns it.
 */

import type { PackageInfo } from "./discovery";

function normRelPath(relPath: string): string {
  return relPath.replace(/\\/g, "/").replace(/^\.\//, "");
}

/**
 * Longest matching package `relativeDir` wins; else root `"."` package if present.
 */
export function resolvePackageForSourceFile(
  relPath: string,
  packages: PackageInfo[],
): PackageInfo | undefined {
  if (packages.length === 0) return undefined;
  const norm = normRelPath(relPath);
  let best: PackageInfo | undefined;
  let bestLen = -1;
  for (const p of packages) {
    if (p.relativeDir === ".") continue;
    const rd = p.relativeDir;
    if (norm === rd || norm.startsWith(rd + "/")) {
      if (rd.length > bestLen) {
        bestLen = rd.length;
        best = p;
      }
    }
  }
  if (best) return best;
  return packages.find((p) => p.relativeDir === ".") ?? packages[0];
}
