import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { discoverProject } from "./discovery";
import { indexProjectStreaming, type LangIndexerJSON } from "./language-indexer";

function indexOne(fileName: string, source: string): LangIndexerJSON {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "jst-bind-"));
  try {
    fs.writeFileSync(
      path.join(dir, "tsconfig.json"),
      JSON.stringify({
        compilerOptions: {
          target: "ES2020",
          module: "ESNext",
          moduleResolution: "node",
          jsx: "react-jsx",
          strict: true,
        },
        include: ["*.ts", "*.tsx"],
      }),
    );
    fs.writeFileSync(path.join(dir, "package.json"), JSON.stringify({ name: "t", private: true }));
    fs.writeFileSync(path.join(dir, fileName), source);

    const discovery = discoverProject(dir);
    const entries: LangIndexerJSON[] = [];
    indexProjectStreaming(dir, discovery, { frameworks: "none" }, (e) => entries.push(e));
    const hit = entries.find((e) => e.path === fileName);
    expect(hit, `no entry for ${fileName}`).toBeDefined();
    return hit!;
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

// Regression for the run of 2026-09-01: VariableDeclaration.getName() returns the SOURCE TEXT of
// the binding name, so `const { rows, summary } = useOrders([...])` was stored as the symbol
// `src.pages.OrdersPage.{ rows, summary }`. The Go planner took it as unit gap 20, generated a test
// for it, and that artifact resolved to the same test path as the real OrdersPage gap.
describe("variable symbols: binding patterns are not symbols", () => {
  it("emits no symbol for an object binding pattern", () => {
    const entry = indexOne(
      "OrdersPage.tsx",
      `export function OrdersPage() {
  const { rows, summary } = { rows: [1], summary: 'x' };
  return rows.length + summary.length;
}
`,
    );
    const bad = entry.symbols.filter((s) => /[{}]/.test(s.fq_name));
    expect(bad.map((s) => s.fq_name)).toEqual([]);
  });

  it("emits no symbol for an array binding pattern", () => {
    const entry = indexOne(
      "Counter.tsx",
      `export function Counter() {
  const [count, setCount] = [0, (n: number) => n];
  return count + Number(!!setCount);
}
`,
    );
    const bad = entry.symbols.filter((s) => s.fq_name.includes("[") || s.fq_name.includes("{"));
    expect(bad.map((s) => s.fq_name)).toEqual([]);
  });

  it("still emits plain identifier variables", () => {
    const entry = indexOne("routes.ts", `export const announcementRoutes = [{ path: '/a' }];\n`);
    expect(entry.symbols.some((s) => s.fq_name.endsWith("announcementRoutes"))).toBe(true);
  });

  it("still emits arrow-function variables as FUNCTION symbols", () => {
    const entry = indexOne("fees.ts", `export const computeFee = (cents: number) => cents * 2;\n`);
    const fn = entry.symbols.find((s) => s.fq_name.endsWith("computeFee"));
    expect(fn).toBeDefined();
    expect(fn!.kind).toBe("FUNCTION");
  });
});
