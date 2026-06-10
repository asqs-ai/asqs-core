/**
 * Parse `--frameworks` and decide which enrichers run (with discovery hints for `auto`).
 * See docs/PLAN.md §2 (indexer backlog).
 */

import type { ProjectDiscovery } from "./discovery";

export type FrameworksMode = "auto" | "none" | Set<string>;

/** Parse CLI `--frameworks` value. */
export function parseFrameworksFlag(raw: string): FrameworksMode {
  const s = raw.trim().toLowerCase();
  if (!s || s === "auto") {
    return "auto";
  }
  if (s === "none") {
    return "none";
  }
  const parts = s
    .split(/[,|]/)
    .map((p) => p.trim())
    .filter(Boolean);
  return new Set(parts);
}

/** Built-ins, entrypoints, CLI symbols (`enrichers-node.ts`). */
export function wantNodeEnrichers(mode: FrameworksMode): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return true;
  }
  return mode.has("node");
}

export function wantReact(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.react;
  }
  return mode.has("react") && d.frameworkSignals.react;
}

export function wantNest(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.nest;
  }
  return mode.has("nest") && d.frameworkSignals.nest;
}

/** TanStack Router `createFileRoute` indexing (`@tanstack/react-router`). */
export function wantTanStackRouter(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.tanstackRouter;
  }
  return mode.has("tanstack");
}

export function wantAngular(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.angular;
  }
  return mode.has("angular") && d.frameworkSignals.angular;
}

export function wantAngularJs(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.angularjs;
  }
  return mode.has("angularjs") && d.frameworkSignals.angularjs;
}

export function wantVue(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.vue;
  }
  return mode.has("vue") && d.frameworkSignals.vue;
}

export function wantSolid(mode: FrameworksMode, d: ProjectDiscovery): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return d.frameworkSignals.solid;
  }
  return mode.has("solid") && d.frameworkSignals.solid;
}

/** `fetch` / axios-style `API_CLIENT_REQUEST` enricher. */
export function wantHttpClientEnricher(mode: FrameworksMode): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return true;
  }
  return mode.has("http");
}

/** Playwright/Cypress `E2E_SPEC` + selectors. */
export function wantE2EEnricher(mode: FrameworksMode): boolean {
  if (mode === "none") {
    return false;
  }
  if (mode === "auto") {
    return true;
  }
  return mode.has("e2e");
}

/** Return known tokens for error messages / docs (not exhaustive). */
export function frameworksHelpTokens(): string {
  return "auto|none|comma-list: node,nest,react,angular,angularjs,vue,solid,http,e2e,tanstack";
}
