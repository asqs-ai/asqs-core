/**
 * Collect source file paths without loading a full TypeScript project.
 * Fast filesystem scan with exclude list (node_modules, transpiled/build dirs, config/tooling).
 */

import * as fs from "fs";
import * as path from "path";

const SOURCE_EXTENSIONS = new Set([
  ".ts",
  ".tsx",
  ".js",
  ".jsx",
  ".mjs",
  ".cjs",
  ".vue",
  ".html",
  ".htm",
]);
/** Directory (segment) names that are always skipped. Segment-only: no path prefixes (e.g. "node_modules", "dist").
 * Do not skip e2e / e2e-tests / __tests__ / cypress: those trees hold Playwright/Cypress/Jest specs; skipping the segment
 * drops specs from the index so E2E_SPEC symbols never appear (same class of bug as Go ScanRepoForFiles had).
 */
export const SKIP_DIRS = new Set(
  [
    "node_modules",
    ".git",
    "dist",
    "public",
    "build",
    "bin",
    "obj",
    "wwwroot",
    "out",
    "target",
    ".next",
    ".nuxt",
    ".output",
    ".svelte-kit",
    ".astro",
    "coverage",
    "website",
    ".nx",
    ".angular",
    ".turbo",
    ".vite",
    ".parcel-cache",
    ".cache",
    ".serverless",
    "angular",
    "angular-animate",
    "angular-loader",
    "angular-mocks",
    "angular-resource",
    "angular-route",
    "jquery",
    "bootstrap",
    "html5-boilerplate",
    "stories",
    "storybook",
    ".storybook",
  ].map((s) => s.toLowerCase()),
);

/** Returns true if any path segment (directory or file) is in SKIP_DIRS. Use when filtering a file list that was not built via getSourceFileList (e.g. ts-morph project with tsconfig). Normalizes path to forward slashes before splitting. */
export function pathHasSkipDirSegment(relPathNorm: string): boolean {
  const normalized = toForwardSlash(relPathNorm);
  const segments = normalized.split("/").filter(Boolean);
  for (const seg of segments) {
    if (SKIP_DIRS.has(seg.toLowerCase())) return true;
  }
  return false;
}

const CONFIG_TOOLING_BASENAMES = new Set(
  [
    "gulpfile.js",
    "gulpfile.ts",
    "gruntfile.js",
    "jest.config.js",
    "jest.config.ts",
    "jest.config.mjs",
    "jest.config.cjs",
    "jest.preset.js",
    "cypress.config.ts",
    "cypress.config.js",
    "postcss.config.js",
    "postcss.config.ts",
    "postcss.config.cjs",
    "webpack.config.js",
    "webpack.config.ts",
    "webpack.config.mjs",
    "vite.config.js",
    "vite.config.ts",
    "next.config.js",
    "next.config.mjs",
    "next.config.ts",
    "nuxt.config.js",
    "nuxt.config.ts",
    "tailwind.config.js",
    "tailwind.config.ts",
    "babel.config.js",
    "babel.config.cjs",
    "babel.config.mjs",
    "rollup.config.js",
    "rollup.config.mjs",
    "karma.conf.js",
    "karma.conf.ts",
    "playwright.config.js",
    "playwright.config.ts",
    "vitest.config.js",
    "vitest.config.ts",
    "vitest.config.mjs",
    "vue.config.js",
    "vue.config.ts",
    "metro.config.js",
    "metro.config.ts",
    "rspack.config.js",
    "rspack.config.ts",
    "eslint.config.js",
    "eslint.config.mjs",
    ".eslintrc.js",
    "stylelint.config.js",
    "stylelint.config.cjs",
    "prettier.config.js",
    "prettier.config.cjs",
    "prettier.config.mjs",
    "tsup.config.ts",
    "tsup.config.js",
    "unocss.config.ts",
    "unocss.config.js",
    "test-setup.ts",
    ".eslintrc.cjs",
    "vite-env.d.ts",
  ].map((s) => s.toLowerCase()),
);
const CONFIG_FILENAME_PATTERN = /\.(config|conf)\.(js|ts|mjs|cjs|jsx|tsx)$/i;

/** Returns true if the path is a config/tooling file (e.g. webpack.config.js, jest.config.ts) that should not be indexed. */
export function isConfigOrToolingPath(relPath: string): boolean {
  const normalized = toForwardSlash(relPath);
  const base = path.basename(normalized).toLowerCase();
  return (
    CONFIG_TOOLING_BASENAMES.has(base) || CONFIG_FILENAME_PATTERN.test(base)
  );
}

function toForwardSlash(p: string): string {
  return p.split(path.sep).join("/");
}

/** Returns true if the file should be skipped: any path segment is in SKIP_DIRS, or basename is config/tooling. */
export function shouldSkipFile(relPathNorm: string): boolean {
  const norm = toForwardSlash(relPathNorm);
  // .d.ts is ambient declarations only — not testable implementation (path.extname is ".ts" for foo.d.ts).
  if (norm.toLowerCase().endsWith(".d.ts")) return true;
  if (pathHasSkipDirSegment(norm)) return true;
  if (isConfigOrToolingPath(norm)) return true;
  return false;
}

/**
 * Returns relative paths (forward slashes) of source files under repoRoot.
 * Excludes: dirs in SKIP_DIRS (by segment name), config/tooling basenames.
 */
export function getSourceFileList(repoRoot: string): string[] {
  const absRoot = path.resolve(repoRoot);
  const out: string[] = [];

  function walk(dir: string): void {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of entries) {
      const full = path.join(dir, e.name);
      if (e.isDirectory()) {
        if (SKIP_DIRS.has(e.name.toLowerCase())) continue;
        walk(full);
      } else if (e.isFile()) {
        const rel = path.relative(absRoot, full);
        const relNorm = toForwardSlash(rel);
        const ext = path.extname(e.name).toLowerCase();
        if (!SOURCE_EXTENSIONS.has(ext)) continue;
        if (shouldSkipFile(relNorm)) continue;
        out.push(relNorm);
      }
    }
  }

  walk(absRoot);
  return out;
}
