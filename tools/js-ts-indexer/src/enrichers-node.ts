/**
 * Phase 3 — Node.js: built-in module imports, package entry files, CLI bin targets.
 */

import { SyntaxKind } from "ts-morph";
import type { SourceFile } from "ts-morph";
import type { PackageInfo } from "./discovery";
import type { FileSymbolsEdges } from "./enrichers";

/** Node built-in modules (without `node:` prefix); includes subpaths like `fs/promises`. */
const NODE_BUILTIN_SPECIFIERS = new Set<string>([
  "assert",
  "assert/strict",
  "async_hooks",
  "buffer",
  "child_process",
  "cluster",
  "console",
  "constants",
  "crypto",
  "dgram",
  "diagnostics_channel",
  "dns",
  "dns/promises",
  "domain",
  "events",
  "fs",
  "fs/promises",
  "http",
  "http2",
  "https",
  "inspector",
  "inspector/promises",
  "module",
  "net",
  "os",
  "path",
  "path/posix",
  "path/win32",
  "perf_hooks",
  "process",
  "punycode",
  "querystring",
  "readline",
  "readline/promises",
  "repl",
  "stream",
  "stream/consumers",
  "stream/promises",
  "stream/web",
  "string_decoder",
  "sys",
  "timers",
  "timers/promises",
  "tls",
  "trace_events",
  "tty",
  "url",
  "util",
  "util/types",
  "v8",
  "vm",
  "wasi",
  "worker_threads",
  "zlib",
]);

function normalizeBuiltinSpecifier(spec: string): string | undefined {
  const s = spec.trim();
  if (!s) return undefined;
  if (s.startsWith("node:")) {
    const rest = s.slice("node:".length);
    return rest ? `node:${rest}` : undefined;
  }
  if (NODE_BUILTIN_SPECIFIERS.has(s)) {
    return `node:${s}`;
  }
  return undefined;
}

/**
 * Emit BUILTIN_MODULE_USE symbols for imports of Node core modules.
 */
export function enrichBuiltinImports(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  for (const imp of sf.getImportDeclarations()) {
    const spec = imp.getModuleSpecifierValue();
    if (!spec) continue;
    const normalized = normalizeBuiltinSpecifier(spec);
    if (!normalized) continue;
    const line = imp.getStartLineNumber();
    const fq = `BUILTIN_USE:${moduleFq}:${normalized}:L${line}`;
    entry.symbols.push({
      kind: "BUILTIN_MODULE_USE",
      fq_name: fq,
      start_line: line,
      end_line: imp.getEndLineNumber(),
      signature: { specifier: spec, resolved: normalized },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: fq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: fq,
      callee_fq_name: normalized,
      edge_type: "USES_BUILTIN",
    });
  }
}

/**
 * Mark package entry files and CLI bin targets from package.json (see discovery PackageInfo).
 */
export function enrichNodePackageSurface(
  entry: FileSymbolsEdges,
  relPathPosix: string,
  moduleFq: string,
  pkg: PackageInfo | undefined,
): void {
  if (!pkg) return;
  const norm = relPathPosix.replace(/\\/g, "/");
  const roles: string[] = [];
  if (pkg.mainEntry && norm === pkg.mainEntry) roles.push("main");
  if (pkg.moduleEntry && norm === pkg.moduleEntry) roles.push("module");
  if (
    pkg.packageDefaultEntry &&
    norm === pkg.packageDefaultEntry &&
    !pkg.mainEntry &&
    !pkg.moduleEntry
  ) {
    roles.push("index");
  }
  if (roles.length > 0) {
    const fq = `ENTRYPOINT:${pkg.name}`;
    entry.symbols.push({
      kind: "ENTRYPOINT",
      fq_name: fq,
      start_line: 1,
      end_line: 1,
      signature: {
        package: pkg.name,
        roles,
        module_kind: pkg.moduleKind,
        path: norm,
      },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: fq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: pkg.name,
      callee_fq_name: fq,
      edge_type: "PACKAGE_ENTRY",
    });
  }

  for (const b of pkg.binEntries ?? []) {
    if (b.relPath !== norm) continue;
    const fq = `CLI_CMD:${pkg.name}:${b.command}`;
    entry.symbols.push({
      kind: "CLI_COMMAND",
      fq_name: fq,
      start_line: 1,
      end_line: 1,
      signature: {
        package: pkg.name,
        command: b.command,
        target_file: norm,
      },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: fq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: pkg.name,
      callee_fq_name: fq,
      edge_type: "DECLARES_CLI",
    });
  }
}

/**
 * Optional: `require("fs")` / `import()` string literals (lightweight heuristic).
 */
export function enrichBuiltinRequireCalls(sf: SourceFile, entry: FileSymbolsEdges, moduleFq: string): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const expr = node.getExpression();
    const args = node.getArguments();
    if (args.length !== 1) return;
    const arg0 = args[0];
    if (!arg0.isKind(SyntaxKind.StringLiteral)) return;
    const lit = arg0.getLiteralValue();
    const normalized = normalizeBuiltinSpecifier(lit);
    if (!normalized) return;
    if (expr.getText() !== "require") return;
    const line = node.getStartLineNumber();
    const fq = `BUILTIN_USE:${moduleFq}:${normalized}:require:L${line}`;
    entry.symbols.push({
      kind: "BUILTIN_MODULE_USE",
      fq_name: fq,
      start_line: line,
      end_line: node.getEndLineNumber(),
      signature: { specifier: lit, resolved: normalized, via: "require" },
    });
    entry.edges.push({
      caller_fq_name: moduleFq,
      callee_fq_name: fq,
      edge_type: "CONTAINS",
    });
    entry.edges.push({
      caller_fq_name: fq,
      callee_fq_name: normalized,
      edge_type: "USES_BUILTIN",
    });
  });
}
