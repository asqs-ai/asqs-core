import { describe, expect, it } from "vitest";
import { dedupeIndexerEdges } from "./dedupe-indexer-edges";

describe("dedupeIndexerEdges", () => {
  it("removes duplicate triples, preserves order of first occurrence", () => {
    const edges = [
      { caller_fq_name: "a", callee_fq_name: "b", edge_type: "CALLS" },
      { caller_fq_name: "a", callee_fq_name: "c", edge_type: "CALLS" },
      { caller_fq_name: "a", callee_fq_name: "b", edge_type: "CALLS" },
      { caller_fq_name: "a", callee_fq_name: "b", edge_type: "CONTAINS" },
    ];
    expect(dedupeIndexerEdges(edges)).toEqual([
      { caller_fq_name: "a", callee_fq_name: "b", edge_type: "CALLS" },
      { caller_fq_name: "a", callee_fq_name: "c", edge_type: "CALLS" },
      { caller_fq_name: "a", callee_fq_name: "b", edge_type: "CONTAINS" },
    ]);
  });
});
