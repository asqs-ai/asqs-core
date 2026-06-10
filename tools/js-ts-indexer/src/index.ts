#!/usr/bin/env node
/**
 * JS/TS indexer — JSONL: one LangIndexerJSON line per file (default stdout, or --jsonl-out <file>).
 * See docs/DESIGN-JS-TS.md and internal/intelligence/indexer/lang.go.
 */

import * as fs from "fs";
import * as path from "path";
import { discoverProject, runtimeLabel, type ProjectDiscovery } from './discovery';
import { indexProjectStreaming } from './language-indexer';
import { buildPackageGraphLines } from './package-graph';
import { buildNuxtFileRouteLines } from './nuxt-file-routes';
import { buildOpenAPIRouteLines } from './openapi-routes';
import { writePhase1Artifacts } from './phase1-artifacts';

const LANG = 'javascript';

/**
 * Write every byte to fd — required for pipes (stdout/stderr to Go). A single fs.writeSync may
 * return early for large buffers; without looping, the remainder is dropped and the next write
 * starts immediately, producing corrupt JSONL (e.g. ..."apps.w{"path":...) and Go errors like
 * invalid character 'p' after object key:value pair.
 */
function writeAllSync(fd: number, data: string | Buffer): void {
  const buf = typeof data === "string" ? Buffer.from(data, "utf8") : data;
  let offset = 0;
  while (offset < buf.length) {
    const written = fs.writeSync(fd, buf, offset, buf.length - offset);
    if (written <= 0) {
      throw new Error(`writeAllSync: wrote ${written} bytes`);
    }
    offset += written;
  }
}

/** Effective FD for stdout: piped Node streams often omit `.fd`; POSIX stdout is still 1. */
function stdoutFD(): number {
  const ws = process.stdout as NodeJS.WriteStream & { fd?: number };
  if (typeof ws.fd === "number" && ws.fd >= 0) {
    return ws.fd;
  }
  return 1;
}

/** Effective FD for stderr when `.fd` is missing. */
function stderrFD(): number {
  const ws = process.stderr as NodeJS.WriteStream & { fd?: number };
  if (typeof ws.fd === "number" && ws.fd >= 0) {
    return ws.fd;
  }
  return 2;
}

/** When set, JSONL records go only here (full-buffer sync writes); stdout stays clean for stderr progress only. */
let jsonlOutFileFd: number | null = null;

/**
 * Write one JSONL record. Uses --jsonl-out file when set; otherwise stdout with full-buffer writes.
 */
function writeOut(line: string): void {
  const buf = Buffer.from(line + "\n", "utf8");
  if (jsonlOutFileFd !== null) {
    writeAllSync(jsonlOutFileFd, buf);
    return;
  }
  const primary = stdoutFD();
  try {
    writeAllSync(primary, buf);
    return;
  } catch {
    // Rare: try literal fd 1 if primary was wrong (e.g. exotic fd assignment).
  }
  try {
    if (primary !== 1) {
      writeAllSync(1, buf);
      return;
    }
  } catch {
    // fall through
  }
  process.stdout.write(line + "\n");
}

/** Write to stderr so output is visible if the process is killed (e.g. OOM). */
function writeErr(msg: string): void {
  const payload = msg.endsWith("\n") ? msg : msg + "\n";
  const buf = Buffer.from(payload, "utf8");
  const primary = stderrFD();
  try {
    writeAllSync(primary, buf);
    return;
  } catch {
    // ignore
  }
  try {
    if (primary !== 2) {
      writeAllSync(2, buf);
      return;
    }
  } catch {
    // ignore
  }
  process.stderr.write(payload);
}

function parseArgs(): { repo: string; output?: string; frameworks: string; jsonlOut?: string } {
  const args = process.argv.slice(2);
  let repo = '';
  let output: string | undefined;
  let frameworks = 'auto';
  let jsonlOut: string | undefined;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--repo' && args[i + 1]) {
      repo = args[++i];
    } else if (args[i] === '--output' && args[i + 1]) {
      output = args[++i];
    } else if (args[i] === '--jsonl-out' && args[i + 1]) {
      jsonlOut = args[++i];
    } else if (args[i] === '--frameworks' && args[i + 1]) {
      frameworks = args[++i];
    }
  }
  return { repo, output, frameworks, jsonlOut };
}

function main(): void {
  const { repo, output, frameworks, jsonlOut } = parseArgs();
  if (!repo) {
    writeErr(
      'Usage: node index.js --repo <path> [--jsonl-out <file>] [--output <dir>] [--frameworks auto|none|node,nest,react,angular,angularjs,vue,solid,http,e2e]',
    );
    process.exit(1);
  }

  if (jsonlOut) {
    const abs = path.resolve(jsonlOut);
    fs.mkdirSync(path.dirname(abs), { recursive: true });
    jsonlOutFileFd = fs.openSync(abs, 'w', 0o644);
  }

  try {
    writeErr('JS/TS indexer: starting discovery...');
    const discovery: ProjectDiscovery = discoverProject(repo);
    writeErr('JS/TS indexer: discovery done, indexing files...');

    let indexedSourceFileCount = 0;
    indexProjectStreaming(repo, discovery, {
      frameworks,
      onProjectLoading() {
        writeErr('JS/TS indexer: loading project (ts-morph full AST; may take a while for large repos)...');
      },
      onStarted(total) {
        writeErr(`JS/TS indexer: found ${total} source files, indexing...`);
      },
      onProgress(n) {
        writeErr(`JS/TS indexer: indexed ${n} files...`);
      },
      onFileStart(relPath, index, total) {
        if (index % 50 === 0 || index < 5) {
          writeErr(`JS/TS indexer: indexing (${index + 1}/${total}): ${relPath}`);
        }
      },
    }, (entry) => {
      indexedSourceFileCount += 1;
      writeOut(JSON.stringify(entry));
    });

    const packageLines = buildPackageGraphLines(discovery);
    for (const line of packageLines) {
      writeOut(JSON.stringify(line));
    }

    for (const line of buildNuxtFileRouteLines(discovery)) {
      writeOut(JSON.stringify(line));
    }
    for (const line of buildOpenAPIRouteLines(repo)) {
      writeOut(JSON.stringify(line));
    }

    // Project meta for evaluation: runtime (nest/node/react/...), test framework, package manager, scripts.
    const metaLine = {
      path: 'asqs-meta:project',
      lang: LANG,
      module: discovery.packages[0]?.name ?? '',
      is_test: false,
      symbols: [] as unknown[],
      edges: [] as unknown[],
      project_meta: {
        runtime: runtimeLabel(discovery),
        test_framework: discovery.testFramework,
        package_manager: discovery.packageManager,
        scripts: discovery.scripts,
      },
    };
    writeOut(JSON.stringify(metaLine));

    if (output) {
      writePhase1Artifacts(output, repo, discovery, indexedSourceFileCount);
      writeErr(`JS/TS indexer: wrote Phase 1 artifacts to ${path.resolve(output)}`);
    }
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    writeErr(`JS/TS indexer: error: ${msg}`);
    if (err instanceof Error && err.stack) {
      writeErr(err.stack);
    }
    process.exit(1);
  } finally {
    if (jsonlOutFileFd !== null) {
      try {
        fs.closeSync(jsonlOutFileFd);
      } catch {
        // ignore
      }
      jsonlOutFileFd = null;
    }
  }
}

main();
