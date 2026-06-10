/**
 * Resolve tsconfig `extends` chain and shallow-merge `compilerOptions` (child overrides parent).
 * Does not replicate TypeScript's full config resolution (paths, composite, etc.).
 */

import * as fs from "fs";
import * as path from "path";
import { toPosix } from "./workspace-packages";

const MAX_EXTENDS_DEPTH = 12;

function readJson<T>(filePath: string): T | null {
  try {
    return JSON.parse(fs.readFileSync(filePath, "utf-8")) as T;
  } catch {
    return null;
  }
}

function mergeCompilerOptionsShallow(
  base: Record<string, unknown>,
  override: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(override)) {
    if (v !== undefined) {
      out[k] = v;
    }
  }
  return out;
}

function resolveExtendsTarget(fromDir: string, extendsField: string): string | null {
  const raw = extendsField.trim();
  if (!raw) return null;
  let resolved = path.resolve(fromDir, raw);
  if (fs.existsSync(resolved)) {
    return resolved;
  }
  if (!resolved.endsWith(".json")) {
    const withJson = resolved + ".json";
    if (fs.existsSync(withJson)) {
      return withJson;
    }
  }
  return null;
}

/**
 * Walk `extends` from the given tsconfig (leaf) toward base configs.
 * @returns `extendsChain` — repo-relative POSIX paths, **root/base first → leaf last** (merge order).
 * `mergedCompilerOptions` — shallow merge along that chain.
 */
export function resolveTsConfigExtendsChainAndMerge(
  tsconfigAbsPath: string,
  repoRoot: string,
): { extendsChain: string[]; mergedCompilerOptions: Record<string, unknown> } {
  const absRoot = path.resolve(repoRoot);
  const leafToRoot: string[] = [];
  const seen = new Set<string>();
  let current: string | null = path.resolve(tsconfigAbsPath);

  while (current && leafToRoot.length < MAX_EXTENDS_DEPTH) {
    if (seen.has(current)) {
      break;
    }
    seen.add(current);
    leafToRoot.push(current);

    const raw = readJson<{ extends?: string; compilerOptions?: Record<string, unknown> }>(current);
    const ext = raw?.extends;
    if (typeof ext !== "string") {
      break;
    }
    const next = resolveExtendsTarget(path.dirname(current), ext);
    if (!next) {
      break;
    }
    current = next;
  }

  const rootToLeaf = [...leafToRoot].reverse();
  let merged: Record<string, unknown> = {};
  for (const file of rootToLeaf) {
    const raw = readJson<{ compilerOptions?: Record<string, unknown> }>(file);
    const co = raw?.compilerOptions;
    if (co && typeof co === "object" && !Array.isArray(co)) {
      merged = mergeCompilerOptionsShallow(merged, co as Record<string, unknown>);
    }
  }

  const extendsChain = rootToLeaf.map((abs) => {
    const rel = path.relative(absRoot, abs);
    if (rel === "") {
      return ".";
    }
    return toPosix(rel);
  });

  return { extendsChain, mergedCompilerOptions: merged };
}
