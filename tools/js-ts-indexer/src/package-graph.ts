/**
 * Package graph: one LangIndexerJSON line per package (virtual path package://name).
 * PACKAGE symbol + DEPENDS_ON edges to dependency names. Emitted after file lines for retrieval/overview.
 */

import type { ProjectDiscovery } from "./discovery";
import { packageVirtualPath } from "./normalize";
import type { LangIndexerJSON } from "./language-indexer";

export function buildPackageGraphLines(discovery: ProjectDiscovery): LangIndexerJSON[] {
  const lines: LangIndexerJSON[] = [];
  for (const pkg of discovery.packages) {
    const path = packageVirtualPath(pkg.name);
    const symbols = [
      {
        kind: "PACKAGE",
        fq_name: pkg.name,
        start_line: 1,
        end_line: 1,
      },
    ];
    const edges: LangIndexerJSON["edges"] = [];
    if (pkg.mainEntry) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: pkg.mainEntry,
        edge_type: "PACKAGE_MAIN",
      });
    }
    if (pkg.moduleEntry) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: pkg.moduleEntry,
        edge_type: "PACKAGE_MODULE",
      });
    }
    if (pkg.packageDefaultEntry) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: pkg.packageDefaultEntry,
        edge_type: "PACKAGE_DEFAULT_INDEX",
      });
    }
    for (const b of pkg.binEntries ?? []) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: b.relPath,
        edge_type: "PACKAGE_BIN",
      });
    }
    const exportAgg = new Map<string, { subpaths: Set<string>; conditions: Set<string> }>();
    for (const ex of pkg.exportEntries ?? []) {
      let g = exportAgg.get(ex.relPath);
      if (!g) {
        g = { subpaths: new Set<string>(), conditions: new Set<string>() };
        exportAgg.set(ex.relPath, g);
      }
      g.subpaths.add(ex.subpath);
      if (ex.conditions) {
        g.conditions.add(ex.conditions);
      }
    }
    for (const [relPath, g] of exportAgg) {
      const signature: Record<string, unknown> = {
        subpaths: [...g.subpaths].sort(),
      };
      if (g.conditions.size > 0) {
        signature.export_conditions = [...g.conditions].sort();
      }
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: relPath,
        edge_type: "PACKAGE_EXPORT",
        signature,
      });
    }
    for (const dep of pkg.dependencies) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: dep,
        edge_type: "DEPENDS_ON",
      });
    }
    for (const dep of pkg.devDependencies) {
      edges.push({
        caller_fq_name: pkg.name,
        callee_fq_name: dep,
        edge_type: "DEPENDS_ON_DEV",
      });
    }
    lines.push({
      path,
      lang: "javascript",
      module: pkg.name,
      is_test: false,
      symbols,
      edges,
    });
  }
  return lines;
}
