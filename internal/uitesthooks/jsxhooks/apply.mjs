#!/usr/bin/env node
/**
 * Adds `data-testid` attributes to intrinsic JSX elements in TS/JS/TSX/JSX sources.
 *
 * Protocol (batched): reads one JSON object from stdin
 *   { resolveFrom: string[], requests: [{ fileName, source, prefix, maxPerFile }] }
 * and writes
 *   { ok, error?, results: [{ ok, source?, changed?, added?: [{name, line, element}], skipped?: number, error? }] }
 *
 * Rules (see the Go package doc for why each exists):
 *   - intrinsic elements only (lowercase tag name); components are never touched;
 *   - an element that already has data-testid / data-cy / data-test, or a spread attribute, is skipped;
 *   - roles: the outermost element returned by a function or arrow component is "root"; button,
 *     a (as "link"), input, select, textarea, form, nav, main, section, header, footer, aside,
 *     h1–h6 (as "heading"), ul/ol (as "list"), li (as "item"), table, tr (as "row"), img (as "image");
 *   - names are `<prefix>-<role>[-<text>]`, text taken from a literal child, aria-label, placeholder,
 *     name, href or type, unique within the file; document order; capped at maxPerFile;
 *   - the result is re-parsed and refused if parsing got worse.
 *
 * `resolveFrom` lists directories to resolve the `typescript` package from, tried in order.
 */
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import path from "node:path";

function loadTypeScript(resolveFrom) {
  const attempts = [];
  for (const dir of resolveFrom || []) {
    if (!dir) continue;
    try {
      const req = createRequire(pathToFileURL(path.join(dir, "__asqs_hooks_resolver__.cjs")));
      return { ts: req("typescript"), from: dir };
    } catch (e) {
      attempts.push(`${dir}: ${e && e.message ? e.message : e}`);
    }
  }
  try {
    return { ts: createRequire(import.meta.url)("typescript"), from: "script" };
  } catch (e) {
    attempts.push(`script: ${e && e.message ? e.message : e}`);
  }
  const err = new Error(
    "cannot resolve the 'typescript' package. Tried:\n  " + attempts.join("\n  ") +
    "\nInstall typescript in the repository under test, or set ASQS_UI_TEST_HOOKS_NODE_PATH."
  );
  throw err;
}

function readStdin() {
  return new Promise((resolve, reject) => {
    const chunks = [];
    process.stdin.on("data", (c) => chunks.push(c));
    process.stdin.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    process.stdin.on("error", reject);
  });
}

const HOOK_ATTRS = new Set(["data-testid", "data-cy", "data-test"]);
const ROLE_BY_TAG = {
  button: "button", a: "link", input: "input", select: "select", textarea: "textarea", form: "form",
  nav: "nav", main: "main", section: "section", header: "header", footer: "footer", aside: "aside",
  h1: "heading", h2: "heading", h3: "heading", h4: "heading", h5: "heading", h6: "heading",
  ul: "list", ol: "list", li: "item", table: "table", tr: "row", img: "image",
};
const TEXT_ATTRS = ["aria-label", "placeholder", "name", "href", "type"];
const MAX_SLUG = 24;

function kebab(s) {
  return String(s)
    .replace(/([a-z0-9])([A-Z])/g, "$1-$2")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function slug(text) {
  let k = kebab(String(text).trim());
  if (k.length > MAX_SLUG) k = k.slice(0, MAX_SLUG).replace(/-+$/g, "");
  return k;
}

function makeApplier(ts) {
  function scriptKind(fileName) {
    const lower = String(fileName).toLowerCase();
    if (lower.endsWith(".tsx")) return ts.ScriptKind.TSX;
    if (lower.endsWith(".jsx")) return ts.ScriptKind.JSX;
    if (lower.endsWith(".js") || lower.endsWith(".mjs") || lower.endsWith(".cjs")) return ts.ScriptKind.JS;
    return ts.ScriptKind.TS;
  }

  function tagNameOf(el) {
    // el is JsxOpeningElement or JsxSelfClosingElement.
    return el.tagName;
  }

  function isIntrinsic(tagName) {
    return ts.isIdentifier(tagName) && /^[a-z][a-z0-9]*$/.test(tagName.text);
  }

  function attributes(el) {
    return el.attributes && el.attributes.properties ? el.attributes.properties : [];
  }

  function hasHookOrSpread(el) {
    for (const p of attributes(el)) {
      if (ts.isJsxSpreadAttribute(p)) return true;
      if (ts.isJsxAttribute(p)) {
        const name = p.name.getText();
        if (HOOK_ATTRS.has(name)) return true;
      }
    }
    return false;
  }

  function literalAttr(el, name, sf) {
    for (const p of attributes(el)) {
      if (!ts.isJsxAttribute(p) || p.name.getText() !== name || !p.initializer) continue;
      const init = p.initializer;
      if (ts.isStringLiteral(init)) return init.text;
      if (ts.isJsxExpression(init) && init.expression &&
          (ts.isStringLiteral(init.expression) || ts.isNoSubstitutionTemplateLiteral(init.expression))) {
        return init.expression.text;
      }
    }
    return "";
  }

  // Literal text directly inside the element (JsxText children only); "" when the content is an
  // expression or nested markup, so a dynamic label never leaks into a name.
  function literalText(openingEl) {
    const parent = openingEl.parent;
    if (!parent || !ts.isJsxElement(parent)) return "";
    let text = "";
    for (const c of parent.children) {
      if (ts.isJsxText(c)) text += c.text;
      else if (ts.isJsxExpression(c) && c.expression && ts.isStringLiteral(c.expression)) text += c.expression.text;
      else return "";
    }
    return text.trim();
  }

  // The outermost element returned by each function/arrow component: its `return (<X>...)` or
  // the arrow body when it is a JSX expression directly.
  function rootElements(sf) {
    const roots = new Set();
    function mark(expr) {
      let e = expr;
      while (e && ts.isParenthesizedExpression(e)) e = e.expression;
      if (!e) return;
      if (ts.isJsxElement(e)) roots.add(e.openingElement);
      else if (ts.isJsxSelfClosingElement(e)) roots.add(e);
      // A fragment root has no tag to attribute; its first element child is the practical root.
      else if (ts.isJsxFragment(e)) {
        for (const c of e.children) {
          if (ts.isJsxElement(c)) { roots.add(c.openingElement); break; }
          if (ts.isJsxSelfClosingElement(c)) { roots.add(c); break; }
        }
      }
    }
    function visitFn(fn) {
      if (!fn.body) return;
      if (!ts.isBlock(fn.body)) { mark(fn.body); return; }
      // Every `return` of the function body, not only the last: early returns for loading and
      // error states are pages of their own to an E2E spec.
      function walk(n) {
        if (ts.isFunctionLike(n) && n !== fn) return; // nested functions have their own roots
        if (ts.isReturnStatement(n) && n.expression) mark(n.expression);
        ts.forEachChild(n, walk);
      }
      walk(fn.body);
    }
    // A component is a top-level declaration: `function X()`, `const X = () =>`, `export default
    // () =>`. An arrow inside JSX or passed to a call (`items.map((it) => <li/>)`) is a render
    // callback, and its element is an item of the enclosing component, not a root of its own.
    function isTopLevelComponentFn(n) {
      if (ts.isFunctionDeclaration(n)) {
        return n.parent && ts.isSourceFile(n.parent);
      }
      if (ts.isArrowFunction(n) || ts.isFunctionExpression(n)) {
        let p = n.parent;
        while (p && (ts.isParenthesizedExpression(p) || ts.isAsExpression(p) || ts.isSatisfiesExpression?.(p))) p = p.parent;
        if (!p) return false;
        if (ts.isVariableDeclaration(p)) {
          const stmt = p.parent && p.parent.parent; // VariableDeclarationList → VariableStatement
          return !!(stmt && stmt.parent && ts.isSourceFile(stmt.parent));
        }
        if (ts.isExportAssignment(p)) return true;
        // React.memo(() => …) / forwardRef(() => …) at the top level.
        if (ts.isCallExpression(p) && p.parent && ts.isVariableDeclaration(p.parent)) {
          const stmt = p.parent.parent && p.parent.parent.parent;
          return !!(stmt && stmt.parent && ts.isSourceFile(stmt.parent));
        }
      }
      return false;
    }
    function visit(n) {
      if ((ts.isFunctionDeclaration(n) || ts.isFunctionExpression(n) || ts.isArrowFunction(n)) && isTopLevelComponentFn(n)) visitFn(n);
      ts.forEachChild(n, visit);
    }
    visit(sf);
    return roots;
  }

  function apply(source, fileName, prefix, maxPerFile) {
    const kind = scriptKind(fileName);
    const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, kind);
    const before = (sf.parseDiagnostics || []).length;
    const roots = rootElements(sf);
    const names = new Map();
    const take = (base) => {
      const n = (names.get(base) || 0) + 1;
      names.set(base, n);
      return n === 1 ? base : `${base}-${n}`;
    };
    const cap = Number(maxPerFile) > 0 ? Number(maxPerFile) : 25;
    const inserts = []; // {pos, text, name, line, element}
    let skipped = 0;

    function consider(el) {
      if (inserts.length >= cap) return;
      const tag = tagNameOf(el);
      if (!isIntrinsic(tag)) return;
      const tagText = tag.text;
      let role = roots.has(el) ? "root" : ROLE_BY_TAG[tagText];
      if (!role) return;
      if (hasHookOrSpread(el)) { skipped++; return; }
      let text = "";
      if (role !== "root") {
        text = literalText(el);
        for (const a of TEXT_ATTRS) {
          if (text) break;
          text = literalAttr(el, a, sf);
        }
      }
      const name = take([prefix, role, slug(text)].filter(Boolean).join("-"));
      const line = ts.getLineAndCharacterOfPosition(sf, el.getStart(sf)).line + 1;
      inserts.push({ pos: tag.end, text: ` data-testid="${name}"`, name, line, element: tagText });
    }

    function visit(n) {
      if (ts.isJsxOpeningElement(n) || ts.isJsxSelfClosingElement(n)) consider(n);
      ts.forEachChild(n, visit);
    }
    visit(sf);

    if (inserts.length === 0) return { ok: true, source, changed: false, added: [], skipped };
    // Apply from the end so earlier positions stay valid.
    let out = source;
    for (const ins of [...inserts].sort((a, b) => b.pos - a.pos)) {
      out = out.slice(0, ins.pos) + ins.text + out.slice(ins.pos);
    }
    const sf2 = ts.createSourceFile(fileName, out, ts.ScriptTarget.Latest, true, kind);
    if ((sf2.parseDiagnostics || []).length > before) {
      return { ok: false, error: "insertion introduced a parse error; file left unchanged" };
    }
    return {
      ok: true, source: out, changed: true, skipped,
      added: inserts.map(({ name, line, element }) => ({ name, line, element })),
    };
  }

  return { apply };
}

const raw = await readStdin();
let input;
try {
  input = JSON.parse(raw);
} catch (e) {
  process.stdout.write(JSON.stringify({ ok: false, error: `invalid JSON on stdin: ${e.message}` }));
  process.exit(0);
}
const requests = Array.isArray(input?.requests) ? input.requests : [];
if (requests.length === 0) {
  process.stdout.write(JSON.stringify({ ok: false, error: "no requests supplied" }));
  process.exit(0);
}
let ts;
try {
  ({ ts } = loadTypeScript(input.resolveFrom));
} catch (e) {
  process.stdout.write(JSON.stringify({ ok: false, error: String(e && e.message ? e.message : e) }));
  process.exit(0);
}
const { apply } = makeApplier(ts);
const results = requests.map((r) => {
  if (!r || !r.fileName || typeof r.source !== "string" || !r.prefix) {
    return { ok: false, error: "missing fileName, source or prefix" };
  }
  try {
    return apply(r.source, r.fileName, r.prefix, r.maxPerFile);
  } catch (e) {
    return { ok: false, error: String(e && e.message ? e.message : e) };
  }
});
process.stdout.write(JSON.stringify({ ok: true, results }));
