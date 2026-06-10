/**
 * Node.js "exports" conditional resolution (static enumeration).
 *
 * Models nested condition objects as in
 * https://nodejs.org/api/packages.html#conditional-exports — object key order is the condition
 * match order; we enumerate every leaf `./…` specifier reachable along a path of condition keys
 * (import, require, node, types, default, custom runtimes, etc.).
 *
 * This is not runtime resolution for a single consumer: it is the *union* of valid targets used to
 * build an accurate package→file graph without flattening unrelated branches into false positives.
 */

export type ExportLeaf = {
  /** Relative specifier from package root, e.g. `./dist/index.mjs`. */
  specifier: string;
  /** Condition keys from root of this export entry, outermost first (JSON insertion order). */
  conditions: string[];
};

function dedupeLeaves(leaves: ExportLeaf[]): ExportLeaf[] {
  const seen = new Set<string>();
  const out: ExportLeaf[] = [];
  for (const x of leaves) {
    const k = `${x.specifier}\0${x.conditions.join(">")}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(x);
  }
  return out;
}

/**
 * Enumerate all `./…` leaf specifiers under one export entry value (string | object | array).
 * `inheritedConds` is the condition chain from parent objects (empty at top of each subpath entry).
 */
export function enumerateConditionalExportLeaves(val: unknown, inheritedConds: readonly string[]): ExportLeaf[] {
  if (val === null || val === undefined) {
    return [];
  }
  if (typeof val === "string") {
    const t = val.trim();
    if (t.startsWith(".")) {
      return [{ specifier: t, conditions: [...inheritedConds] }];
    }
    return [];
  }
  if (Array.isArray(val)) {
    const acc: ExportLeaf[] = [];
    for (const el of val) {
      acc.push(...enumerateConditionalExportLeaves(el, inheritedConds));
    }
    return dedupeLeaves(acc);
  }
  if (typeof val !== "object") {
    return [];
  }
  const o = val as Record<string, unknown>;
  const keys = Object.keys(o);
  if (keys.length === 0) {
    return [];
  }
  const acc: ExportLeaf[] = [];
  for (const key of keys) {
    const child = o[key];
    if (child === null || child === undefined) {
      continue;
    }
    acc.push(...enumerateConditionalExportLeaves(child, [...inheritedConds, key]));
  }
  return dedupeLeaves(acc);
}

/** Format condition chain for logs / PACKAGE_EXPORT signatures (Node uses `>` in some docs for nesting). */
export function formatExportConditionChain(conditions: readonly string[]): string | undefined {
  if (conditions.length === 0) {
    return undefined;
  }
  return conditions.join(">");
}
