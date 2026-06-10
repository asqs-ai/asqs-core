/**
 * Collapse duplicate graph edges (same caller + callee + edge_type).
 * Large JS files (e.g. monolithic classes) can emit thousands of identical CALLS edges;
 * deduping shrinks JSONL lines and avoids pipe / capture truncation issues.
 */

export type IndexerEdge = {
  caller_fq_name: string;
  callee_fq_name: string;
  edge_type: string;
};

export function dedupeIndexerEdges<T extends IndexerEdge>(edges: T[]): T[] {
  if (edges.length <= 1) return edges;
  const seen = new Set<string>();
  const out: T[] = [];
  for (const e of edges) {
    const k = `${e.caller_fq_name}\0${e.callee_fq_name}\0${e.edge_type}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(e);
  }
  return out;
}
