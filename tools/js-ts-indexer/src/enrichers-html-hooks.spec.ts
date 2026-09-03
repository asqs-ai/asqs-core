import { describe, expect, it } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { indexHtmlTemplateFile } from "./enrichers-html-hooks";

describe("indexHtmlTemplateFile", () => {
  it("returns null when no hooks", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "html-idx-"));
    const f = path.join(dir, "empty.html");
    fs.writeFileSync(f, "<html><body></body></html>", "utf8");
    expect(indexHtmlTemplateFile(dir, "empty.html")).toBeNull();
  });

  it("emits STATIC_TEMPLATE and UI_TEST_HOOK for data-testid", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "html-idx-"));
    const f = path.join(dir, "page.html");
    fs.writeFileSync(
      f,
      `<div data-testid="login-btn">x</div>\n<span data-cy="submit"></span>`,
      "utf8",
    );
    const j = indexHtmlTemplateFile(dir, "page.html");
    expect(j).not.toBeNull();
    expect(j!.lang).toBe("html");
    expect(j!.symbols.some((s) => s.kind === "STATIC_TEMPLATE")).toBe(true);
    const hooks = j!.symbols.filter((s) => s.kind === "UI_TEST_HOOK");
    expect(hooks.length).toBeGreaterThanOrEqual(2);
    expect(hooks.some((h) => h.signature && (h.signature as { value?: string }).value === "login-btn")).toBe(true);
  });

  // A selector alone answers "what exists" and says nothing about "when". Every remaining E2E
  // failure in run api-c81d90a22d1460d87b64e483837fdc24 was a REAL selector asserted at the wrong
  // moment: catalog-loading lives inside `@if (loading)` and is gone the instant the search
  // resolves.
  it("marks selectors inside @if as conditional and inside @for as repeated", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "html-idx-"));
    const f = path.join(dir, "catalog.html");
    fs.writeFileSync(
      f,
      `<section data-testid="catalog-root">
  <h1 data-testid="catalog-title">Catalog</h1>
  @if (loading) {
    <p data-testid="catalog-loading">Loading…</p>
  }
  <ul data-testid="catalog-results">
    @for (item of results; track item.id) {
      <li>
        {{ item.name }} —
        <span data-testid="catalog-item-price">{{ item.unitPrice }}</span>
      </li>
    }
  </ul>
  <p data-testid="catalog-footer">after the blocks</p>
</section>`,
      "utf8",
    );
    const j = indexHtmlTemplateFile(dir, "catalog.html")!;
    const by = new Map(
      j.symbols
        .filter((s) => s.kind === "UI_TEST_HOOK")
        .map((s) => [(s.signature as { value: string }).value, s.signature as { conditional: boolean; repeated: boolean }]),
    );

    expect(by.get("catalog-root")).toMatchObject({ conditional: false, repeated: false });
    expect(by.get("catalog-title")).toMatchObject({ conditional: false, repeated: false });
    expect(by.get("catalog-loading")).toMatchObject({ conditional: true, repeated: false });
    // The <ul> itself is always in the DOM; only its contents repeat.
    expect(by.get("catalog-results")).toMatchObject({ conditional: false, repeated: false });
    expect(by.get("catalog-item-price")).toMatchObject({ conditional: true, repeated: true });
    // Everything after the closing brace is unconditional again — the block must not leak.
    expect(by.get("catalog-footer")).toMatchObject({ conditional: false, repeated: false });
  });

  // THE BUG THE BRACE STRIPPER EXISTS FOR. `{{ item.name }}` contributes two closing braces; a
  // naive counter popped the enclosing @for on that line and reported everything after it as
  // unconditional.
  it("does not treat interpolation or attribute braces as block delimiters", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "html-idx-"));
    const f = path.join(dir, "interp.html");
    fs.writeFileSync(
      f,
      `@for (item of items; track item.id) {
  {{ item.name }}
  <span title="{{ item.hint }}" data-testid="deep-inside">x</span>
}
<p data-testid="outside">done</p>`,
      "utf8",
    );
    const j = indexHtmlTemplateFile(dir, "interp.html")!;
    const by = new Map(
      j.symbols
        .filter((s) => s.kind === "UI_TEST_HOOK")
        .map((s) => [(s.signature as { value: string }).value, s.signature as { conditional: boolean; repeated: boolean }]),
    );
    expect(by.get("deep-inside")).toMatchObject({ conditional: true, repeated: true });
    expect(by.get("outside")).toMatchObject({ conditional: false, repeated: false });
  });

  // Legacy structural directives gate the element they sit on rather than opening a block.
  it("marks *ngIf and *ngFor elements", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "html-idx-"));
    const f = path.join(dir, "legacy.html");
    fs.writeFileSync(
      f,
      `<p *ngIf="loading" data-testid="legacy-loading">Loading…</p>
<li *ngFor="let r of rows" data-testid="legacy-row"></li>
<p data-testid="legacy-plain"></p>`,
      "utf8",
    );
    const j = indexHtmlTemplateFile(dir, "legacy.html")!;
    const by = new Map(
      j.symbols
        .filter((s) => s.kind === "UI_TEST_HOOK")
        .map((s) => [(s.signature as { value: string }).value, s.signature as { conditional: boolean; repeated: boolean }]),
    );
    expect(by.get("legacy-loading")).toMatchObject({ conditional: true, repeated: false });
    expect(by.get("legacy-row")).toMatchObject({ conditional: true, repeated: true });
    expect(by.get("legacy-plain")).toMatchObject({ conditional: false, repeated: false });
  });

});
