/**
 * Resolve npm/pnpm/yarn workspace patterns to absolute package.json paths.
 */

import * as fs from "fs";
import * as path from "path";

/** Normalize to forward slashes for pattern matching. */
export function toPosix(p: string): string {
  return p.split(path.sep).join("/");
}

/**
 * Read `pnpm-workspace.yaml` package glob patterns (no yaml dependency).
 */
export function readPnpmWorkspacePatterns(repoRoot: string): string[] {
  const p = path.join(repoRoot, "pnpm-workspace.yaml");
  if (!fs.existsSync(p)) {
    return [];
  }
  let text: string;
  try {
    text = fs.readFileSync(p, "utf-8");
  } catch {
    return [];
  }
  const patterns: string[] = [];
  let inPackages = false;
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (trimmed.startsWith("#")) continue;
    if (/^packages:\s*$/.test(trimmed)) {
      inPackages = true;
      continue;
    }
    if (inPackages) {
      if (trimmed === "") continue;
      if (trimmed.startsWith("-")) {
        const m = trimmed.match(/^-\s*(?:['"]([^'"]+)['"]|(\S+))\s*$/);
        const g = (m?.[1] ?? m?.[2] ?? "").trim();
        if (g) patterns.push(toPosix(g));
        continue;
      }
      // Another top-level key ends the packages block
      if (/^[a-zA-Z_][\w-]*:/.test(trimmed)) {
        break;
      }
    }
  }
  return patterns;
}

const WORKSPACE_SKIP_DIRS = new Set([
  "node_modules",
  ".git",
  "dist",
  "build",
  ".next",
  "coverage",
  "out",
  "target",
]);

function collectPackageJsonUnderDir(absDir: string, maxDepth: number): string[] {
  const out: string[] = [];
  const walk = (dir: string, depth: number): void => {
    if (depth > maxDepth) return;
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const ent of entries) {
      const name = ent.name;
      if (name === "." || name === "..") continue;
      if (name.startsWith(".") && name !== ".") continue;
      const p = path.join(dir, name);
      if (ent.isDirectory()) {
        if (WORKSPACE_SKIP_DIRS.has(name)) continue;
        walk(p, depth + 1);
      } else if (name === "package.json") {
        out.push(path.resolve(p));
      }
    }
  };
  walk(absDir, 0);
  return out;
}

/**
 * Expand a single workspace glob relative to repo root. Supports:
 * - `packages/*` (one directory level)
 * - `packages/**` or `**` (nested packages; skips common build/vendor dirs)
 * - `apps/web` (exact package folder)
 */
export function expandWorkspacePattern(repoRoot: string, pattern: string): string[] {
  const posix = pattern.replace(/\\/g, "/").trim();
  if (!posix) return [];

  if (posix === "**") {
    return collectPackageJsonUnderDir(path.resolve(repoRoot), 12).sort();
  }

  if (posix.endsWith("/**")) {
    const baseRel = posix.slice(0, -3).replace(/\/+$/, "");
    const baseDir = baseRel === "" ? path.resolve(repoRoot) : path.join(repoRoot, baseRel);
    if (!fs.existsSync(baseDir) || !fs.statSync(baseDir).isDirectory()) {
      return [];
    }
    return collectPackageJsonUnderDir(path.resolve(baseDir), 12).sort();
  }

  if (posix.endsWith("/*")) {
    const baseRel = posix.slice(0, -2);
    const baseDir = path.join(repoRoot, baseRel);
    if (!fs.existsSync(baseDir) || !fs.statSync(baseDir).isDirectory()) {
      return [];
    }
    const out: string[] = [];
    for (const ent of fs.readdirSync(baseDir, { withFileTypes: true })) {
      if (!ent.isDirectory()) continue;
      const pj = path.join(baseDir, ent.name, "package.json");
      if (fs.existsSync(pj)) {
        out.push(path.resolve(pj));
      }
    }
    return out;
  }

  if (posix.includes("*")) {
    // Unsupported glob; skip (avoid silent wrong results)
    return [];
  }

  const dir = path.join(repoRoot, posix);
  const pj = path.join(dir, "package.json");
  if (fs.existsSync(pj)) {
    return [path.resolve(pj)];
  }
  return [];
}

export function normalizeWorkspacesField(workspaces: unknown): string[] {
  if (workspaces == null) return [];
  if (typeof workspaces === "string") return [toPosix(workspaces)];
  if (Array.isArray(workspaces)) {
    return workspaces.filter((x): x is string => typeof x === "string").map((x) => toPosix(x));
  }
  return [];
}

/**
 * All workspace member package.json paths (absolute), excluding root (add root separately).
 */
export function collectWorkspacePackageJsonPaths(repoRoot: string, rootWorkspaces: unknown): string[] {
  const absRoot = path.resolve(repoRoot);
  const patterns = new Set<string>();
  for (const w of normalizeWorkspacesField(rootWorkspaces)) {
    patterns.add(w);
  }
  for (const w of readPnpmWorkspacePatterns(absRoot)) {
    patterns.add(w);
  }

  const seen = new Set<string>();
  const paths: string[] = [];
  for (const pat of patterns) {
    for (const pj of expandWorkspacePattern(absRoot, pat)) {
      const norm = path.resolve(pj);
      if (seen.has(norm)) continue;
      seen.add(norm);
      paths.push(norm);
    }
  }
  return paths.sort();
}
