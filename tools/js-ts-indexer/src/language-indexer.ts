/**
 * Language indexer: full AST via ts-morph Project.
 * One MODULE per file; CONTAINS, IMPORTS (callee = resolved file module id), EXTENDS, IMPLEMENTS, CALLS; optional React/Nest enrichers.
 * Streams one LangIndexerJSON per file to onFile() after loading the full project.
 */

import * as fs from "fs";
import * as path from "path";
import { Project, SyntaxKind } from "ts-morph";
import type { ProjectDiscovery } from "./discovery";
import {
  parseFrameworksFlag,
  wantAngular,
  wantAngularJs,
  wantE2EEnricher,
  wantHttpClientEnricher,
  wantNest,
  wantNodeEnrichers,
  wantReact,
  wantSolid,
  wantTanStackRouter,
  wantVue,
} from "./framework-runtime";
import { enrichFileE2ESpec, isLikelyE2ESpecPath } from "./enrichers-e2e";
import { enrichFileHttpClient } from "./enrichers-http-client";
import { indexHtmlTemplateFile } from "./enrichers-html-hooks";
import { enrichAngularTemplateAst } from "./enrichers-angular-template-ast";
import { enrichNestDtoGuardsPipes } from "./enrichers-nest-dto-guards";
import { enrichNestModuleGraph } from "./enrichers-nest-graph";
import { enrichTanStackFileRoutes } from "./enrichers-tanstack-router";
import {
  enrichBuiltinImports,
  enrichBuiltinRequireCalls,
  enrichNodePackageSurface,
} from "./enrichers-node";
import { enrichFileNest, enrichFileReact } from "./enrichers";
import { enrichFileAngularGraph } from "./enrichers-angular-graph";
import { enrichFileAngularJsGraph } from "./enrichers-angularjs-graph";
import { enrichFileAngularRoutes } from "./enrichers-angular-routes";
import { enrichFileReactRouter } from "./enrichers-react-routes";
import { enrichFileSolidRouter } from "./enrichers-solid-routes";
import { enrichFileVueRouter } from "./enrichers-vue-routes";
import {
  enrichCallableTypeRefs,
  enrichDefaultExports,
  enrichEnums,
  enrichInterfaceMemberTypeRefs,
  enrichReExports,
  enrichSameFileNamedExports,
  enrichTestBlocks,
  enrichTypeAliases,
  signatureWithFunctionExportSurface,
  signatureWithInterfaceMethodSurface,
  signatureWithJsdoc,
  signatureWithJsdocForVariable,
  signatureWithMemberVisibility,
  signatureWithNamedDeclarationExport,
} from "./enrichers-semantics";
import { filePathToModuleId, fqName } from "./normalize";
import { resolvePackageForSourceFile } from "./package-resolve";
import { getSourceFileList, isConfigOrToolingPath, shouldSkipFile } from "./file-list";
import { dedupeIndexerEdges } from "./dedupe-indexer-edges";
import { spanColumns1Based, spanColumnsForNode } from "./span-columns";

export interface LangIndexerJSON {
  path: string;
  lang: string;
  module: string;
  is_test: boolean;
  symbols: {
    kind: string;
    fq_name: string;
    start_line: number;
    end_line: number;
    start_column?: number;
    end_column?: number;
    signature?: unknown;
  }[];
  edges: {
    caller_fq_name: string;
    callee_fq_name: string;
    edge_type: string;
    /** Optional metadata (e.g. PACKAGE_EXPORT: subpaths + export_conditions). Preserved in ParsedEdge.SignatureJSON when ingested by Go. */
    signature?: unknown;
  }[];
  di_injected_types?: {
    caller_fq_name: string;
    callee_fq_name: string;
    edge_type: string;
  }[];
  di_registered_services?: {
    caller_fq_name: string;
    callee_fq_name: string;
    edge_type: string;
  }[];
  di_implements_services?: {
    caller_fq_name: string;
    callee_fq_name: string;
    edge_type: string;
  }[];
}

const SOURCE_EXTENSIONS = [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"];
const TEST_PATTERNS = /\.(test|spec)\.(ts|tsx|js|jsx|mjs|cjs)$/i;
const TEST_DIR = /(^|\/)(__tests__|test|tests)(\/|$)/;

function isTestFilePath(relPath: string): boolean {
  // Unit/integration layout (*.test.ts, __tests__/, …) plus E2E trees (*.cy.ts, cypress/, e2e/, playwright paths).
  // Must match Go scan IsLikelyTestSourcePath enough that is_test + E2E_SPEC stay in sync (ListGapsE2E joins on is_test).
  if (TEST_PATTERNS.test(relPath) || TEST_DIR.test(relPath)) return true;
  return isLikelyE2ESpecPath(relPath);
}

/** When package.json discovery missed react but this file still imports the router (monorepo edge cases). */
function fileImportsReactRouter(sf: { getFullText: () => string }): boolean {
  const text = sf.getFullText();
  return (
    /from\s+['"]react-router-dom['"]/.test(text) ||
    /from\s+['"]react-router['"]/.test(text) ||
    /require\s*\(\s*['"]react-router-dom['"]/.test(text) ||
    /require\s*\(\s*['"]react-router['"]/.test(text)
  );
}

function toForwardSlash(p: string): string {
  return p.split(path.sep).join("/");
}

function collectCalls(
  container: { getDescendantsOfKind: (k: number) => unknown[] },
  callerFq: string,
  edges: LangIndexerJSON["edges"],
): void {
  const callNodes = container.getDescendantsOfKind(SyntaxKind.CallExpression);
  for (const call of callNodes) {
    try {
      const c = call as { getExpression: () => { getText: () => string } };
      const expr = c.getExpression();
      const calleeText = expr.getText().trim();
      if (calleeText && !calleeText.startsWith("UNRESOLVED") && calleeText.length < 80 && !/[\r\n]/.test(calleeText)) {
        edges.push({
          caller_fq_name: callerFq,
          callee_fq_name: calleeText,
          edge_type: "CALLS",
        });
      }
    } catch {
      // ignore
    }
  }
}

function collectDIPayload(edges: LangIndexerJSON["edges"]): Pick<
  LangIndexerJSON,
  "di_injected_types" | "di_registered_services" | "di_implements_services"
> {
  const diInjected: NonNullable<LangIndexerJSON["di_injected_types"]> = [];
  const diRegistered: NonNullable<LangIndexerJSON["di_registered_services"]> = [];
  const diImplements: NonNullable<LangIndexerJSON["di_implements_services"]> = [];

  for (const e of edges) {
    const edgeType = String(e.edge_type ?? "").trim().toUpperCase();
    if (!edgeType) continue;
    if (edgeType === "INJECTS" || edgeType === "INJECTS_NAMED") {
      diInjected.push({
        caller_fq_name: e.caller_fq_name,
        callee_fq_name: e.callee_fq_name,
        edge_type: edgeType,
      });
      continue;
    }
    if (edgeType === "REGISTERS_SERVICE") {
      diRegistered.push({
        caller_fq_name: e.caller_fq_name,
        callee_fq_name: e.callee_fq_name,
        edge_type: edgeType,
      });
      continue;
    }
    if (edgeType === "IMPLEMENTS_SERVICE") {
      diImplements.push({
        caller_fq_name: e.caller_fq_name,
        callee_fq_name: e.callee_fq_name,
        edge_type: edgeType,
      });
    }
  }

  return {
    di_injected_types: diInjected.length > 0 ? diInjected : undefined,
    di_registered_services: diRegistered.length > 0 ? diRegistered : undefined,
    di_implements_services: diImplements.length > 0 ? diImplements : undefined,
  };
}

export interface IndexStreamOpts {
  frameworks: string;
  onProjectLoading?: () => void;
  onStarted?: (totalFiles: number) => void;
  onProgress?: (indexedCount: number) => void;
  onFileStart?: (relPath: string, index: number, total: number) => void;
}

/**
 * Load the full project with ts-morph, then stream one LangIndexerJSON per file via onFile().
 */
export function indexProjectStreaming(
  repoRoot: string,
  discovery: ProjectDiscovery,
  opts: IndexStreamOpts,
  onFile: (entry: LangIndexerJSON) => void,
): void {
  const absRoot = path.resolve(repoRoot);
  const tsConfigPath = path.join(absRoot, "tsconfig.json");
  const hasTsConfig = fs.existsSync(tsConfigPath);

  opts.onProjectLoading?.();

  const project = hasTsConfig
    ? new Project({ tsConfigFilePath: tsConfigPath })
    : new Project({
        compilerOptions: { allowJs: true },
        skipAddingFilesFromTsConfig: true,
      });

  if (!hasTsConfig) {
    const fileList = getSourceFileList(absRoot);
    for (const relPath of fileList) {
      project.addSourceFileAtPath(path.join(absRoot, relPath));
    }
  }

  let sourceFiles = project.getSourceFiles();
  // When tsconfig yields 0 files (e.g. empty include, or path resolution issues), fall back to filesystem list.
  if (sourceFiles.length === 0 && hasTsConfig) {
    const fileList = getSourceFileList(absRoot);
    for (const relPath of fileList) {
      project.addSourceFileAtPath(path.join(absRoot, relPath));
    }
    sourceFiles = project.getSourceFiles();
  }

  const absRootNorm = path.resolve(absRoot);
  const rawCount = sourceFiles.length;
  sourceFiles = sourceFiles.filter((sf) => {
    const filePath = sf.getFilePath();
    const filePathAbs = path.isAbsolute(filePath) ? path.resolve(filePath) : path.join(absRootNorm, filePath);
    const relPath = toForwardSlash(path.relative(absRootNorm, filePathAbs));
    if (relPath.startsWith("..") || path.isAbsolute(relPath)) return false;
    return !shouldSkipFile(relPath);
  });
  // If filter excluded everything but we had files (e.g. path.relative quirk), keep files that are not skipped.
  if (sourceFiles.length === 0 && rawCount > 0) {
    sourceFiles = project.getSourceFiles().filter((sf) => {
      const filePath = sf.getFilePath();
      const filePathAbs = path.isAbsolute(filePath) ? path.resolve(filePath) : path.join(absRootNorm, filePath);
      const relPath = toForwardSlash(path.relative(absRootNorm, filePathAbs));
      return !shouldSkipFile(relPath);
    });
  }
  opts.onStarted?.(sourceFiles.length);

  const fwMode = parseFrameworksFlag(opts.frameworks);

  const progressInterval = 50;
  let indexedCount = 0;
  const total = sourceFiles.length;

  for (let i = 0; i < sourceFiles.length; i++) {
    const sf = sourceFiles[i];
    const filePath = sf.getFilePath();
    const filePathAbs = path.isAbsolute(filePath) ? path.resolve(filePath) : path.join(absRootNorm, filePath);
    let relPath = toForwardSlash(path.relative(absRootNorm, filePathAbs));
    if (relPath.startsWith("..") || path.isAbsolute(relPath)) continue;
    if (shouldSkipFile(relPath)) continue;
    const ext = path.extname(relPath).toLowerCase();
    if (!SOURCE_EXTENSIONS.includes(ext)) continue;
    if (isConfigOrToolingPath(relPath)) continue;

    opts.onFileStart?.(relPath, i, total);

    const isTest = isTestFilePath(relPath);
    const moduleId = filePathToModuleId(relPath);
    const moduleFq = moduleId;
    const symbols: LangIndexerJSON["symbols"] = [];
    const edges: LangIndexerJSON["edges"] = [];

    symbols.push({
      kind: "MODULE",
      fq_name: moduleFq,
      start_line: 1,
      end_line: 1,
    });

    const addContains = (callerFq: string, calleeFq: string) => {
      edges.push({
        caller_fq_name: callerFq,
        callee_fq_name: calleeFq,
        edge_type: "CONTAINS",
      });
    };

    const pushExports = (symbolFq: string) => {
      edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: symbolFq,
        edge_type: "EXPORTS",
      });
    };

    // IMPORTS: callee must be another file's MODULE fq_name (e.g. "src.utils") so Go can resolve
    // symbol IDs and the overview file graph works. Raw specifiers ("./foo", "@/bar") never match symbols.
    const seenImportCalleeModules = new Set<string>();
    for (const imp of sf.getImportDeclarations()) {
      const resolvedSf = imp.getModuleSpecifierSourceFile();
      if (!resolvedSf) continue;
      const resPath = resolvedSf.getFilePath();
      const resAbs = path.isAbsolute(resPath) ? path.resolve(resPath) : path.join(absRootNorm, resPath);
      let calleeRel = toForwardSlash(path.relative(absRootNorm, resAbs));
      if (calleeRel.startsWith("..") || path.isAbsolute(calleeRel)) continue;
      if (shouldSkipFile(calleeRel) || isConfigOrToolingPath(calleeRel)) continue;
      const calleeExt = path.extname(calleeRel).toLowerCase();
      if (!SOURCE_EXTENSIONS.includes(calleeExt)) continue;
      const calleeModuleFq = filePathToModuleId(calleeRel);
      if (!calleeModuleFq || calleeModuleFq === moduleFq) continue;
      if (seenImportCalleeModules.has(calleeModuleFq)) continue;
      seenImportCalleeModules.add(calleeModuleFq);
      edges.push({
        caller_fq_name: moduleFq,
        callee_fq_name: calleeModuleFq,
        edge_type: "IMPORTS",
      });
    }

    sf.forEachDescendant((node) => {
      if (node.isKind(SyntaxKind.ClassDeclaration)) {
        const name = node.getName();
        if (!name) return;
        const start = node.getStartLineNumber();
        const end = node.getEndLineNumber();
        const classFq = fqName(moduleId, name);
        const classSig = signatureWithNamedDeclarationExport(signatureWithJsdoc({}, node), node);
        const classSpan = spanColumnsForNode(node);
        symbols.push({
          kind: "CLASS",
          fq_name: classFq,
          start_line: start,
          end_line: end,
          start_column: classSpan.start_column,
          end_column: classSpan.end_column,
          signature: classSig,
        });
        addContains(moduleFq, classFq);
        if (node.isExported()) {
          pushExports(classFq);
        }

        const extClause = node.getExtends();
        const extendsClause = Array.isArray(extClause) ? extClause : extClause !== undefined ? [extClause] : [];
        for (const e of extendsClause) {
          const baseText = e.getText().trim();
          if (baseText) {
            edges.push({
              caller_fq_name: classFq,
              callee_fq_name: baseText,
              edge_type: "EXTENDS",
            });
          }
        }
        const impl = node.getImplements();
        const implementsClause = Array.isArray(impl) ? impl : impl !== undefined ? [impl] : [];
        for (const implNode of implementsClause) {
          const ifaceText = implNode.getText().trim();
          if (ifaceText) {
            edges.push({
              caller_fq_name: classFq,
              callee_fq_name: ifaceText,
              edge_type: "IMPLEMENTS",
            });
          }
        }

        for (const member of node.getMembers()) {
          if (member.isKind(SyntaxKind.MethodDeclaration)) {
            const methodName = (member as { getName?: () => string }).getName?.();
            if (typeof methodName !== "string") continue;
            const mStart = member.getStartLineNumber();
            const mEnd = member.getEndLineNumber();
            const methodFq = `${classFq}.${methodName}`;
            const mSig = signatureWithMemberVisibility(signatureWithJsdoc({}, member), member);
            const mSpan = spanColumnsForNode(member);
            symbols.push({
              kind: "METHOD",
              fq_name: methodFq,
              start_line: mStart,
              end_line: mEnd,
              start_column: mSpan.start_column,
              end_column: mSpan.end_column,
              signature: mSig,
            });
            addContains(classFq, methodFq);
            collectCalls(member, methodFq, edges);
            enrichCallableTypeRefs(methodFq, member, edges);
          }
        }
        return;
      }
      if (node.isKind(SyntaxKind.InterfaceDeclaration)) {
        const name = node.getName();
        if (!name) return;
        const start = node.getStartLineNumber();
        const end = node.getEndLineNumber();
        const fq = fqName(moduleId, name);
        const ifaceSig = signatureWithNamedDeclarationExport(signatureWithJsdoc({}, node), node);
        const ifaceSpan = spanColumnsForNode(node);
        symbols.push({
          kind: "INTERFACE",
          fq_name: fq,
          start_line: start,
          end_line: end,
          start_column: ifaceSpan.start_column,
          end_column: ifaceSpan.end_column,
          signature: ifaceSig,
        });
        addContains(moduleFq, fq);
        if (node.isExported()) {
          pushExports(fq);
        }
        for (const mem of node.getMembers()) {
          if (mem.isKind(SyntaxKind.MethodSignature)) {
            const methodName = mem.getName();
            const mStart = mem.getStartLineNumber();
            const mEnd = mem.getEndLineNumber();
            const methodFq = `${fq}.${methodName}`;
            const msSig = signatureWithInterfaceMethodSurface(signatureWithJsdoc({}, mem), mem);
            const imSpan = spanColumnsForNode(mem);
            symbols.push({
              kind: "INTERFACE_METHOD",
              fq_name: methodFq,
              start_line: mStart,
              end_line: mEnd,
              start_column: imSpan.start_column,
              end_column: imSpan.end_column,
              signature: msSig,
            });
            addContains(fq, methodFq);
            enrichInterfaceMemberTypeRefs(fq, mem, edges);
          }
        }
        return;
      }
      if (node.isKind(SyntaxKind.FunctionDeclaration)) {
        const name = node.getName();
        if (!name) return;
        const start = node.getStartLineNumber();
        const end = node.getEndLineNumber();
        const fq = fqName(moduleId, name);
        const fnSig = signatureWithFunctionExportSurface(signatureWithJsdoc({}, node), node);
        const fnSpan = spanColumnsForNode(node);
        symbols.push({
          kind: "FUNCTION",
          fq_name: fq,
          start_line: start,
          end_line: end,
          start_column: fnSpan.start_column,
          end_column: fnSpan.end_column,
          signature: fnSig,
        });
        addContains(moduleFq, fq);
        collectCalls(node, fq, edges);
        enrichCallableTypeRefs(fq, node, edges);
        if (node.isExported()) {
          pushExports(fq);
        }
        return;
      }
      if (node.isKind(SyntaxKind.VariableStatement)) {
        const declList = node.getDeclarationList();
        for (const decl of declList.getDeclarations()) {
          const name = decl.getName();
          if (typeof name !== "string") continue;
          const init = decl.getInitializer();
          const isCallable =
            init &&
            (init.isKind(SyntaxKind.ArrowFunction) || init.isKind(SyntaxKind.FunctionExpression));
          if (isCallable) {
            // Use the statement start line (the "export const ..." line) so docs are inserted one line above the export.
            const start = node.getStartLineNumber();
            const end = init.getEndLineNumber();
            const fq = fqName(moduleId, name);
            const vSig = signatureWithNamedDeclarationExport(signatureWithJsdocForVariable(decl), node);
            const vFnSpan = spanColumns1Based(node, node.getStart(), init.getEnd());
            symbols.push({
              kind: "FUNCTION",
              fq_name: fq,
              start_line: start,
              end_line: end,
              start_column: vFnSpan.start_column,
              end_column: vFnSpan.end_column,
              signature: vSig,
            });
            addContains(moduleFq, fq);
            collectCalls(init, fq, edges);
            if (init.isKind(SyntaxKind.ArrowFunction)) {
              enrichCallableTypeRefs(fq, init, edges);
            }
            if (node.isExported()) {
              pushExports(fq);
            }
          } else {
            const start = node.getStartLineNumber();
            const end = node.getEndLineNumber();
            const fq = fqName(moduleId, name);
            const vSig = signatureWithNamedDeclarationExport(signatureWithJsdocForVariable(decl), node);
            const vSpan = spanColumnsForNode(node);
            symbols.push({
              kind: "VARIABLE",
              fq_name: fq,
              start_line: start,
              end_line: end,
              start_column: vSpan.start_column,
              end_column: vSpan.end_column,
              signature: vSig,
            });
            addContains(moduleFq, fq);
            if (node.isExported()) {
              pushExports(fq);
            }
          }
        }
      }
    });

    enrichTypeAliases(sf, { symbols, edges }, moduleId, moduleFq, addContains);
    enrichEnums(sf, { symbols, edges }, moduleId, moduleFq, addContains);
    enrichDefaultExports(sf, { symbols, edges }, moduleId, moduleFq);
    enrichReExports(sf, { symbols, edges }, moduleFq);
    enrichSameFileNamedExports(sf, { symbols, edges }, moduleId, moduleFq);
    if (isTest) {
      enrichTestBlocks(sf, { symbols, edges }, moduleFq);
    }

    const pkg = resolvePackageForSourceFile(relPath, discovery.packages);
    if (wantNodeEnrichers(fwMode)) {
      enrichBuiltinImports(sf, { symbols, edges }, moduleFq);
      enrichBuiltinRequireCalls(sf, { symbols, edges }, moduleFq);
      enrichNodePackageSurface({ symbols, edges }, relPath, moduleFq, pkg);
    }

    const entry = { symbols, edges };
    const reactGraph = wantReact(fwMode, discovery);
    const reactRouter =
      reactGraph || (fwMode === "auto" && fileImportsReactRouter(sf));
    if (reactGraph) {
      enrichFileReact(sf, entry, moduleId);
    }
    if (reactRouter) {
      enrichFileReactRouter(sf, entry, moduleFq);
    }
    if (reactGraph && wantTanStackRouter(fwMode, discovery)) {
      enrichTanStackFileRoutes(sf, entry, moduleFq);
    }
    if (wantAngular(fwMode, discovery)) {
      enrichFileAngularRoutes(sf, entry, moduleFq);
      enrichFileAngularGraph(sf, entry, moduleId, moduleFq);
      enrichAngularTemplateAst(sf, entry, moduleFq);
    }
    if (wantAngularJs(fwMode, discovery)) {
      enrichFileAngularJsGraph(sf, entry, moduleFq);
    }
    if (wantVue(fwMode, discovery)) {
      enrichFileVueRouter(sf, entry, moduleFq);
    }
    if (wantSolid(fwMode, discovery)) {
      enrichFileSolidRouter(sf, entry, moduleFq);
    }
    if (wantNest(fwMode, discovery)) {
      enrichFileNest(sf, entry, moduleId);
      enrichNestModuleGraph(sf, entry, moduleId, moduleFq);
      enrichNestDtoGuardsPipes(sf, entry, moduleId, moduleFq);
    }
    if (isTest && wantE2EEnricher(fwMode)) {
      enrichFileE2ESpec(sf, entry, moduleFq, relPath);
    }
    if (!isTest && wantHttpClientEnricher(fwMode)) {
      const srcText = sf.getFullText();
      if (srcText.includes("fetch(") || srcText.includes("axios.")) {
        enrichFileHttpClient(sf, entry, moduleFq);
      }
    }

    const lang = ext === ".ts" || ext === ".tsx" ? "typescript" : "javascript";
    const dedupedEdges = dedupeIndexerEdges(entry.edges);
    const diPayload = collectDIPayload(dedupedEdges);
    onFile({
      path: relPath,
      lang,
      module: moduleId,
      is_test: isTest,
      symbols: entry.symbols,
      edges: dedupedEdges,
      ...diPayload,
    });
    indexedCount++;
    if (opts.onProgress && indexedCount % progressInterval === 0) {
      opts.onProgress(indexedCount);
    }
  }

  // Static HTML templates (public/, src/, etc.): same hook model as Java java_html_hooks.
  const htmlPaths = getSourceFileList(absRoot).filter((rel) => /\.html?$/i.test(rel));
  for (const relPath of htmlPaths) {
    if (shouldSkipFile(relPath)) continue;
    const htmlEntry = indexHtmlTemplateFile(absRoot, relPath);
    if (htmlEntry) {
      onFile(htmlEntry as LangIndexerJSON);
    }
  }
}

/**
 * Non-streaming: collect all entries and return.
 */
export function indexProjectToLangIndexerJSON(
  repoRoot: string,
  discovery: ProjectDiscovery,
  opts: {
    frameworks: string;
    onProjectLoading?: () => void;
    onStarted?: (totalFiles: number) => void;
    onProgress?: (indexedCount: number) => void;
  },
): LangIndexerJSON[] {
  const results: LangIndexerJSON[] = [];
  opts.onProjectLoading?.();
  indexProjectStreaming(repoRoot, discovery, opts, (entry) => results.push(entry));
  return results;
}
