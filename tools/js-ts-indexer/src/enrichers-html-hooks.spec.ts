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
});
