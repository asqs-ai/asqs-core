/**
 * In-document OpenAPI $ref resolution for Path Item and Operation objects (JSON Pointer, RFC 6901).
 * External references (no leading `#`) are not followed — keeps indexing deterministic without I/O.
 */

const MAX_REF_DEPTH = 24;

function jsonPointerResolve(doc: unknown, ref: string): unknown {
  let pointer = ref.startsWith("#") ? ref.slice(1) : ref;
  if (pointer === "") return doc;
  if (!pointer.startsWith("/")) return undefined;
  const segments = pointer.slice(1).split("/");
  let cur: unknown = doc;
  for (const raw of segments) {
    const tok = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (cur === null || typeof cur !== "object") return undefined;
    if (Array.isArray(cur)) {
      const idx = parseInt(tok, 10);
      if (Number.isNaN(idx) || idx < 0 || idx >= cur.length) return undefined;
      cur = cur[idx];
    } else {
      const o = cur as Record<string, unknown>;
      if (!Object.prototype.hasOwnProperty.call(o, tok)) return undefined;
      cur = o[tok];
    }
  }
  return cur;
}

function shallowMergeOpenAPI(base: Record<string, unknown>, overlay: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(overlay)) {
    if (k === "$ref") continue;
    out[k] = v;
  }
  return out;
}

function resolveOpenAPIOperation(
  op: Record<string, unknown>,
  root: Record<string, unknown>,
  depth: number,
  seen: Set<string>,
): Record<string, unknown> {
  let m = shallowMergeOpenAPI(op, {});
  while (depth < MAX_REF_DEPTH) {
    const ref = m.$ref;
    if (typeof ref !== "string" || !ref.startsWith("#")) break;
    if (seen.has(ref)) break;
    seen.add(ref);
    const target = jsonPointerResolve(root, ref);
    seen.delete(ref);
    if (!target || typeof target !== "object" || Array.isArray(target)) break;
    m = shallowMergeOpenAPI(target as Record<string, unknown>, m);
    delete m.$ref;
    depth++;
  }
  return m;
}

function resolveOpenAPIPathItem(val: unknown, root: Record<string, unknown>, depth: number, seen: Set<string>): Record<string, unknown> | null {
  if (!val || typeof val !== "object" || Array.isArray(val)) return null;
  let m = shallowMergeOpenAPI(val as Record<string, unknown>, {});
  while (depth < MAX_REF_DEPTH) {
    const ref = m.$ref;
    if (typeof ref !== "string" || !ref.startsWith("#")) break;
    if (seen.has(ref)) break;
    seen.add(ref);
    const target = jsonPointerResolve(root, ref);
    seen.delete(ref);
    if (!target || typeof target !== "object" || Array.isArray(target)) break;
    m = shallowMergeOpenAPI(target as Record<string, unknown>, m);
    delete m.$ref;
    depth++;
  }
  for (const vk of Object.keys(m)) {
    const vlow = vk.toLowerCase().trim();
    if (!HTTP_VERB_SET.has(vlow)) continue;
    const vv = m[vk];
    if (vv && typeof vv === "object" && !Array.isArray(vv)) {
      m[vk] = resolveOpenAPIOperation(vv as Record<string, unknown>, root, depth + 1, seen);
    }
  }
  return m;
}

/** Set of HTTP methods (lowercase) — must match openapi-routes HTTP_VERBS iteration. */
const HTTP_VERB_SET = new Set(["get", "post", "put", "patch", "delete", "options", "head", "trace"]);

export function expandOpenAPIPathRefs(pathsVal: unknown, root: Record<string, unknown>): unknown {
  if (!pathsVal || typeof pathsVal !== "object" || Array.isArray(pathsVal)) {
    return pathsVal;
  }
  const pathsObj = pathsVal as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(pathsObj)) {
    const resolved = resolveOpenAPIPathItem(v, root, 0, new Set<string>());
    out[k] = resolved ?? v;
  }
  return out;
}
