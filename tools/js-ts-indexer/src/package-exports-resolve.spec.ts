import { describe, expect, it } from "vitest";
import {
  enumerateConditionalExportLeaves,
  formatExportConditionChain,
} from "./package-exports-resolve";

describe("enumerateConditionalExportLeaves", () => {
  it("collects import, require, and default as separate condition paths", () => {
    const leaves = enumerateConditionalExportLeaves(
      {
        import: "./a.mjs",
        require: "./b.cjs",
        default: "./c.js",
      },
      [],
    );
    const bySpec = Object.fromEntries(leaves.map((l) => [l.specifier, l.conditions.join(">")]));
    expect(bySpec["./a.mjs"]).toBe("import");
    expect(bySpec["./b.cjs"]).toBe("require");
    expect(bySpec["./c.js"]).toBe("default");
  });

  it("nests node then import/require", () => {
    const leaves = enumerateConditionalExportLeaves(
      {
        node: {
          import: "./node-esm.mjs",
          require: "./node-cjs.cjs",
        },
        default: "./fallback.js",
      },
      [],
    );
    const chains = leaves.map((l) => `${l.conditions.join(">")}=>${l.specifier}`).sort();
    expect(chains).toEqual([
      "default=>./fallback.js",
      "node>import=>./node-esm.mjs",
      "node>require=>./node-cjs.cjs",
    ]);
  });

  it("supports types alongside import (TypeScript handbook pattern)", () => {
    const leaves = enumerateConditionalExportLeaves(
      {
        types: "./dist/index.d.ts",
        import: "./dist/index.mjs",
        require: "./dist/index.cjs",
      },
      [],
    );
    expect(leaves).toHaveLength(3);
    const types = leaves.find((l) => l.specifier === "./dist/index.d.ts");
    expect(types?.conditions).toEqual(["types"]);
  });

  it("flattens array alternatives with shared inherited conditions", () => {
    const leaves = enumerateConditionalExportLeaves([{ import: "./a.mjs" }, "./b.js"], []);
    const specs = leaves.map((l) => l.specifier).sort();
    expect(specs).toEqual(["./a.mjs", "./b.js"]);
    expect(leaves.find((l) => l.specifier === "./a.mjs")?.conditions).toEqual(["import"]);
    expect(leaves.find((l) => l.specifier === "./b.js")?.conditions).toEqual([]);
  });

  it("preserves key order for condition precedence documentation", () => {
    const leaves = enumerateConditionalExportLeaves(
      {
        require: "./r.cjs",
        import: "./i.mjs",
      },
      [],
    );
    expect(leaves.map((l) => l.conditions[0])).toEqual(["require", "import"]);
  });
});

describe("formatExportConditionChain", () => {
  it("returns undefined for empty", () => {
    expect(formatExportConditionChain([])).toBeUndefined();
  });
  it("joins with >", () => {
    expect(formatExportConditionChain(["node", "import"])).toBe("node>import");
  });
});
