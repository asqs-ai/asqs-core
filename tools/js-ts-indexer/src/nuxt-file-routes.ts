/**
 * Nuxt 3 file-based routing: derive URL patterns from `pages/` tree `.vue` paths (discovery.nuxtPagePaths).
 * Emits PAGE_ROUTE symbols so ListGapsE2E / retrieval see the same route vocabulary as code-based routers.
 * See https://nuxt.com/docs/guide/directory-structure/pages
 */

import type { ProjectDiscovery } from "./discovery";
import type { LangIndexerJSON } from "./language-indexer";
import { normalizeHttpPath } from "./enrichers";
import { pageRouteFQName } from "./enrichers-page-route-common";
import { filePathToModuleId } from "./normalize";

/** Map `pages/.../*.vue` to a URL pattern (leading slash, dynamic segments as :param). */
export function nuxtPagePathToRoutePattern(relPath: string): string | undefined {
  const norm = relPath.replace(/\\/g, "/");
  if (!norm.toLowerCase().endsWith(".vue")) return undefined;
  const lower = norm.toLowerCase();
  if (!lower.startsWith("pages/")) return undefined;

  const inner = norm.slice("pages/".length, -".vue".length);
  if (inner === "" || inner.toLowerCase() === "index") {
    return "/";
  }

  const parts = inner.split("/").filter(Boolean);
  if (parts.length === 0) return "/";

  const urlSegs: string[] = [];
  for (let i = 0; i < parts.length; i++) {
    const seg = parts[i];
    if (seg.toLowerCase() === "index" && i === parts.length - 1) {
      continue;
    }
    // Catch-all: [...slug].vue
    if (seg.startsWith("[...") && seg.endsWith("]")) {
      const name = seg.slice(4, -1).trim() || "pathMatch";
      urlSegs.push(`:${name}(.*)`);
      continue;
    }
    // Optional param: [[slug]].vue
    if (seg.startsWith("[[") && seg.endsWith("]]")) {
      const name = seg.slice(2, -2).trim() || "optional";
      urlSegs.push(`:${name}?`);
      continue;
    }
    // Dynamic: [id].vue
    if (seg.startsWith("[") && seg.endsWith("]")) {
      urlSegs.push(":" + seg.slice(1, -1).trim());
      continue;
    }
    urlSegs.push(seg);
  }

  if (urlSegs.length === 0) {
    return "/";
  }
  return normalizeHttpPath("", "/" + urlSegs.join("/"));
}

export function buildNuxtFileRouteLines(discovery: ProjectDiscovery): LangIndexerJSON[] {
  if (!discovery.frameworkSignals?.nuxt || !discovery.nuxtPagePaths?.length) {
    return [];
  }
  const out: LangIndexerJSON[] = [];
  for (const rel of discovery.nuxtPagePaths) {
    const pattern = nuxtPagePathToRoutePattern(rel);
    if (!pattern) continue;
    const moduleFq = filePathToModuleId(rel);
    const line = 1;
    const fq = pageRouteFQName(pattern, moduleFq, line);
    const pat = normalizeHttpPath("", pattern);
    out.push({
      path: rel,
      lang: "typescript",
      module: moduleFq,
      is_test: false,
      symbols: [
        { kind: "MODULE", fq_name: moduleFq, start_line: 1, end_line: 1 },
        {
          kind: "PAGE_ROUTE",
          fq_name: fq,
          start_line: line,
          end_line: line,
          signature: {
            framework: "nuxt-file-route",
            path_pattern: pat,
            source: "pages-directory",
          },
        },
      ],
      edges: [
        { caller_fq_name: moduleFq, callee_fq_name: fq, edge_type: "CONTAINS" },
      ],
    });
  }
  return out;
}
