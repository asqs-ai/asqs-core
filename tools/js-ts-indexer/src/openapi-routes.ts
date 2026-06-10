/**
 * OpenAPI 3 / Swagger 2: emit API_ROUTE symbols from `paths` (JSON or YAML) so they join the same
 * method+path namespace as Nest/Spring routes and API_CLIENT_REQUEST → TARGETS_API_ROUTE linking.
 *
 * Per OpenAPI Specification, the document is a single data model with JSON and YAML as alternative
 * serializations (same `paths` object). We parse both; for each logical stem (`openapi` / `swagger` per
 * directory), **JSON is preferred** when multiple files exist (`.json` before `.yaml` / `.yml`).
 */

import * as fs from "fs";
import * as path from "path";
import { parse as parseYaml } from "yaml";
import type { LangIndexerJSON } from "./language-indexer";
import { normalizeHttpPath } from "./enrichers";
import { filePathToModuleId } from "./normalize";
import { expandOpenAPIPathRefs } from "./openapi-pathrefs";

const HTTP_VERBS = new Set([
  "get",
  "post",
  "put",
  "patch",
  "delete",
  "options",
  "head",
  "trace",
]);

/** Repo-relative directories (empty = repository root) × `openapi` / `swagger` × extension order. */
const SPEC_DIRS = [
  "",
  "api",
  "docs",
  "src",
  "src/main/resources",
  "openapi",
  "spec",
  "specs",
  "contracts",
  "rest-api",
];
const SPEC_NAMES = ["openapi", "swagger"] as const;
const SPEC_EXT_ORDER = [".json", ".yaml", ".yml"] as const;

export type OpenAPIRouteOp = { method: string; path: string; operationId?: string };

function extractOpsFromPathsObject(pathsVal: unknown): OpenAPIRouteOp[] {
  if (!pathsVal || typeof pathsVal !== "object" || Array.isArray(pathsVal)) {
    return [];
  }
  const pathsObj = pathsVal as Record<string, unknown>;
  const out: OpenAPIRouteOp[] = [];
  for (const [pathKey, item] of Object.entries(pathsObj)) {
    if (typeof pathKey !== "string" || !pathKey.startsWith("/")) continue;
    const normPath = normalizeHttpPath("", pathKey);
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    for (const [verb, opVal] of Object.entries(item as Record<string, unknown>)) {
      const vlow = verb.toLowerCase();
      if (!HTTP_VERBS.has(vlow)) continue;
      let operationId: string | undefined;
      if (opVal && typeof opVal === "object" && !Array.isArray(opVal)) {
        const oid = (opVal as Record<string, unknown>).operationId;
        if (typeof oid === "string" && oid.trim()) operationId = oid.trim();
      }
      out.push({ method: vlow.toUpperCase(), path: normPath, operationId });
    }
  }
  return out;
}

function extractOpsFromDocumentRoot(root: Record<string, unknown>): OpenAPIRouteOp[] {
  const pathsExpanded = expandOpenAPIPathRefs(root.paths, root);
  return extractOpsFromPathsObject(pathsExpanded);
}

/** Parse JSON document; returns operations from `paths` (empty if invalid or no paths). */
export function extractOpenAPIRoutesFromJSON(data: string): OpenAPIRouteOp[] {
  let root: Record<string, unknown>;
  try {
    root = JSON.parse(data) as Record<string, unknown>;
  } catch {
    return [];
  }
  return extractOpsFromDocumentRoot(root);
}

/**
 * Parse YAML document (OpenAPI 3 / Swagger 2); returns operations from `paths`.
 * Uses strict-ish parsing via the `yaml` package (YAML 1.2).
 */
export function extractOpenAPIRoutesFromYAML(data: string): OpenAPIRouteOp[] {
  let root: unknown;
  try {
    root = parseYaml(data);
  } catch {
    return [];
  }
  if (!root || typeof root !== "object" || Array.isArray(root)) {
    return [];
  }
  return extractOpsFromDocumentRoot(root as Record<string, unknown>);
}

/**
 * Detect serialization: JSON object/array first when the trimmed document looks like JSON; otherwise YAML.
 * If JSON yields no operations, YAML is attempted (covers empty `paths` in JSON vs richer YAML — rare).
 */
export function extractOpenAPIRoutesFromSpecContent(data: string): OpenAPIRouteOp[] {
  const trimmed = data.replace(/^\uFEFF/, "").trim();
  if (!trimmed) return [];
  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    try {
      const root = JSON.parse(data) as Record<string, unknown>;
      const ops = extractOpsFromDocumentRoot(root);
      if (ops.length > 0) return ops;
    } catch {
      /* fall through */
    }
  }
  return extractOpenAPIRoutesFromYAML(data);
}

function specRelPath(dir: string, baseName: string, ext: string): string {
  const file = `${baseName}${ext}`;
  if (!dir) return file.replace(/\\/g, "/");
  return path.posix.join(dir.replace(/\\/g, "/"), file);
}

function nestApiRouteFQName(httpMethod: string, fullPath: string, handlerAnchor: string): string {
  const m = httpMethod.toUpperCase();
  const p = normalizeHttpPath("", fullPath);
  return `API_ROUTE:${m}:${p}@${handlerAnchor}`;
}

function emitSpecLines(relPosix: string, routes: OpenAPIRouteOp[]): LangIndexerJSON {
  const moduleFq = filePathToModuleId(relPosix);
  const symbols: LangIndexerJSON["symbols"] = [{ kind: "MODULE", fq_name: moduleFq, start_line: 1, end_line: 1 }];
  const edges: LangIndexerJSON["edges"] = [];

  for (let i = 0; i < routes.length; i++) {
    const r = routes[i];
    const line = i + 2;
    const anchor = `openapi:${relPosix}#${r.method}_${r.path}`;
    const fq = nestApiRouteFQName(r.method, r.path, anchor);
    const sig: Record<string, string> = {
      framework: "openapi",
      spec: relPosix,
      http_method: r.method,
      path: r.path,
    };
    if (r.operationId) sig.operation_id = r.operationId;
    symbols.push({
      kind: "API_ROUTE",
      fq_name: fq,
      start_line: line,
      end_line: line,
      signature: sig,
    });
    edges.push({ caller_fq_name: moduleFq, callee_fq_name: fq, edge_type: "CONTAINS" });
  }

  return {
    path: relPosix,
    lang: "javascript",
    module: moduleFq,
    is_test: false,
    symbols,
    edges,
  };
}

/**
 * Walk configured stems; for each, pick the first existing file in order `.json` → `.yaml` → `.yml`
 * that yields at least one operation (or skip stem if path already satisfied in a future check — none here).
 */
export function buildOpenAPIRouteLines(repoRoot: string): LangIndexerJSON[] {
  const absRoot = path.resolve(repoRoot);
  const lines: LangIndexerJSON[] = [];

  for (const dir of SPEC_DIRS) {
    for (const name of SPEC_NAMES) {
      for (const ext of SPEC_EXT_ORDER) {
        const rel = specRelPath(dir, name, ext);
        const full = path.join(absRoot, ...rel.split("/"));
        if (!fs.existsSync(full) || !fs.statSync(full).isFile()) continue;
        const data = fs.readFileSync(full, "utf8");
        const routes = extractOpenAPIRoutesFromSpecContent(data);
        if (routes.length === 0) continue;
        lines.push(emitSpecLines(rel, routes));
        break;
      }
    }
  }

  return lines;
}
