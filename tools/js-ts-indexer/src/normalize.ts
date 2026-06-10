/**
 * Normalized identifiers for symbols and modules (stable FQ names for retrieval).
 */

import * as path from "path";

/**
 * File path to a stable module id: strip extension, replace path separators with dots, drop leading dot.
 * e.g. "src/foo/bar.ts" -> "src.foo.bar", "index.ts" -> "index"
 */
export function filePathToModuleId(relPath: string): string {
  const noExt = relPath.replace(/\.[^.]+$/, "");
  const normalized = noExt.split(/[/\\]/).filter(Boolean).join(".");
  return normalized || "root";
}

/**
 * FQ name for a symbol in a file: moduleId.symbolName (or just symbolName if module is root).
 */
export function fqName(moduleId: string, symbolName: string): string {
  if (!moduleId || moduleId === "root") return symbolName;
  return `${moduleId}.${symbolName}`;
}

/**
 * Package virtual path for emission (Go can persist as synthetic file).
 */
export function packageVirtualPath(packageName: string): string {
  return `package://${packageName}`;
}
