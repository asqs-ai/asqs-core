/**
 * Static HTML / server-template hooks: data-testid, data-cy, basic Thymeleaf test ids.
 * Emits STATIC_TEMPLATE + UI_TEST_HOOK + CONTAINS (parity with Java java_html_hooks).
 */

import * as fs from "fs";
import * as path from "path";

/** Same shape as LangIndexerJSON (avoid circular import with language-indexer). */
export interface HtmlLangIndexerJSON {
  path: string;
  lang: string;
  module: string;
  is_test: boolean;
  symbols: {
    kind: string;
    fq_name: string;
    start_line: number;
    end_line: number;
    signature?: unknown;
  }[];
  edges: {
    caller_fq_name: string;
    callee_fq_name: string;
    edge_type: string;
  }[];
}

const PATTERNS: { re: RegExp; selectorKind: string; framework: string }[] = [
  { re: /data-testid\s*=\s*["']([^"']+)["']/gi, selectorKind: "data-testid", framework: "html" },
  { re: /data-cy\s*=\s*["']([^"']+)["']/gi, selectorKind: "data-cy", framework: "cypress_template" },
  { re: /th:data-testid\s*=\s*["']([^"']+)["']/gi, selectorKind: "data-testid", framework: "thymeleaf" },
  { re: /th:testid\s*=\s*["']([^"']+)["']/gi, selectorKind: "testid", framework: "thymeleaf" },
];

function templateModuleFq(relPath: string): string {
  const posix = relPath.split(path.sep).join("/");
  const noExt = posix.replace(/\.html?$/i, "");
  return `template.content.${noExt.replace(/\//g, ".")}`;
}

function uiHookFqToken(s: string): string {
  return s
    .replace(/[@:/\\\s]/g, "_")
    .slice(0, 64);
}

/**
 * Build LangIndexerJSON for one .html file (read from disk).
 * Returns null when no testability hooks found (parity with Java java_html_hooks).
 */
export function indexHtmlTemplateFile(absRoot: string, relPath: string): HtmlLangIndexerJSON | null {
  const full = path.join(absRoot, relPath);
  const raw = fs.readFileSync(full, "utf8");
  const lines = raw.split(/\n/);
  const modFq = templateModuleFq(relPath);
  const pathKey = relPath.split(path.sep).join("/");
  const tplFq = `STATIC_TEMPLATE:${pathKey}`;
  const maxLine = Math.max(1, lines.length);

  const hookSymbols: HtmlLangIndexerJSON["symbols"] = [];
  const hookEdges: HtmlLangIndexerJSON["edges"] = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    const lineNum = i + 1;
    for (const { re, selectorKind, framework } of PATTERNS) {
      const r = new RegExp(re.source, re.flags.includes("g") ? re.flags : `${re.flags}g`);
      let m: RegExpExecArray | null;
      while ((m = r.exec(line)) !== null) {
        const val = (m[1] ?? "").trim();
        if (!val) continue;
        const hookFq = `UI_TEST_HOOK:${selectorKind}:${uiHookFqToken(val)}@${pathKey}:L${lineNum}`;
        hookSymbols.push({
          kind: "UI_TEST_HOOK",
          fq_name: hookFq,
          start_line: lineNum,
          end_line: lineNum,
          signature: {
            selector_kind: selectorKind,
            value: val,
            framework,
            template_path: pathKey,
          },
        });
        hookEdges.push({
          caller_fq_name: tplFq,
          callee_fq_name: hookFq,
          edge_type: "CONTAINS",
        });
      }
    }
  }

  if (hookSymbols.length === 0) {
    return null;
  }

  const symbols: HtmlLangIndexerJSON["symbols"] = [
    {
      kind: "MODULE",
      fq_name: modFq,
      start_line: 1,
      end_line: maxLine,
    },
    {
      kind: "STATIC_TEMPLATE",
      fq_name: tplFq,
      start_line: 1,
      end_line: maxLine,
      signature: {
        template_path: pathKey,
        facet: "static_or_server_template",
      },
    },
    ...hookSymbols,
  ];
  const edges: HtmlLangIndexerJSON["edges"] = [
    {
      caller_fq_name: modFq,
      callee_fq_name: tplFq,
      edge_type: "CONTAINS",
    },
    ...hookEdges,
  ];

  const low = pathKey.toLowerCase();
  const isTest = low.includes("/test/") || low.includes("__tests__/");
  return {
    path: pathKey,
    lang: "html",
    module: modFq.replace(/^template\.content\./, ""),
    is_test: isTest,
    symbols,
    edges,
  };
}
