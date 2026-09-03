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
 * Angular control-flow block openers. `@if` / `@else` / `@switch` / `@case` gate whether their
 * contents are in the DOM at all; `@for` repeats them.
 */
const NG_BLOCK_RE = /@(if|else|for|switch|case|empty)\b/g;

/** Legacy structural directives, which gate the ELEMENT they sit on rather than a block. */
const NG_STRUCTURAL_ATTR_RE = /\*ngIf\b|\*ngFor\b|\*ngSwitchCase\b|\*ngSwitchDefault\b/;

/**
 * templateRenderState tracks whether the line currently being scanned sits inside Angular control
 * flow, so a selector can be reported as conditional or repeated rather than as a flat fact.
 *
 * Why it matters, from run api-c81d90a22d1460d87b64e483837fdc24: every remaining E2E failure was a
 * real selector asserted at the wrong moment. `catalog-loading` lives inside `@if (loading)` and is
 * gone once the search resolves; `catalog-results` is a `<ul>` that is always in the DOM but
 * reports hidden while empty, because an empty list has no height. A generator told only that both
 * selectors exist writes `toBeVisible()` on both and fails on both.
 *
 * Brace counting on the source text, deliberately: an HTML template is not parsed here, and a
 * scanner that stays line-oriented cannot be wrong about anything except nesting it never saw. A
 * block opened and closed on one line is handled because the closer is counted on that same line.
 */
function stripNonStructuralBraces(line: string): string {
  // Interpolations first: `{{ item.name }}` contributes two closing braces that would pop a block
  // the line never opened — the bug this strip exists for. An interpolation inside an attribute
  // value is removed here before the value itself is.
  return line
    .replace(/\{\{[\s\S]*?\}\}/g, "")
    .replace(/"[^"]*"/g, '""')
    .replace(/'[^']*'/g, "''");
}

function scanTemplateControlFlow(lines: string[]): { conditional: boolean; repeated: boolean }[] {
  const out: { conditional: boolean; repeated: boolean }[] = [];
  // Stack of open blocks; each entry says whether that block repeats its contents.
  const open: { repeats: boolean }[] = [];
  for (const raw of lines) {
    const line = raw ?? "";
    // Braces belonging to interpolations or attribute values are not block delimiters.
    const code = stripNonStructuralBraces(line);
    // State BEFORE this line's own closers, so the line carrying `}` still counts as inside.
    const enclosing = open.slice();

    let opened = 0;
    NG_BLOCK_RE.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = NG_BLOCK_RE.exec(code)) !== null) {
      // Only count a block that actually opens a brace on this line; `@if` inside a comment or an
      // interpolation without `{` is not a block.
      if (code.indexOf("{", m.index) < 0) continue;
      open.push({ repeats: m[1] === "for" });
      opened++;
    }
    const closers = (code.match(/}/g) ?? []).length;
    for (let i = 0; i < closers && open.length > 0; i++) {
      open.pop();
    }

    // A structural directive gates its own element, so the line carrying it is conditional even
    // when no block is open.
    const structural = NG_STRUCTURAL_ATTR_RE.test(line);
    const repeats = /\*ngFor\b/.test(line);
    const inherited = opened > 0 ? open.slice(0, open.length) : enclosing;

    out.push({
      conditional: structural || inherited.length > 0,
      repeated: repeats || inherited.some((b) => b.repeats),
    });
  }
  return out;
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
  const renderState = scanTemplateControlFlow(lines);

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    const lineNum = i + 1;
    const state = renderState[i] ?? { conditional: false, repeated: false };
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
            // Whether the element is on the page at all depends on state; a consumer that only
            // knows the selector exists will assert on it at the wrong moment.
            conditional: state.conditional,
            repeated: state.repeated,
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
