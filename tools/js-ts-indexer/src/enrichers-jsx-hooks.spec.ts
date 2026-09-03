import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileJsxHooks } from "./enrichers-jsx-hooks";
import type { FileSymbolsEdges } from "./enrichers";

type Sig = { selector_kind: string; value: string; template_path: string; conditional: boolean; repeated: boolean };

function hooksOf(source: string, relPath = "src/pages/HomePage.tsx"): Map<string, Sig> {
  const project = new Project({ useInMemoryFileSystem: true });
  const sf = project.createSourceFile(relPath, source.trim());
  const entry: FileSymbolsEdges = { symbols: [], edges: [] };
  enrichFileJsxHooks(sf, entry, relPath);
  const out = new Map<string, Sig>();
  for (const s of entry.symbols) {
    if (s.kind !== "UI_TEST_HOOK") continue;
    const sig = s.signature as Sig;
    out.set(`${sig.selector_kind}:${sig.value}`, sig);
  }
  return out;
}

describe("enrichFileJsxHooks", () => {
  // The React run of 2026-09-03 had zero selectors in its inventory because React markup lives in
  // .tsx, not in a template file. The same UI_TEST_HOOK shape the HTML enricher emits must come out
  // of JSX attributes.
  it("emits UI_TEST_HOOK for literal data-testid, data-cy and aria-label attributes", () => {
    const hooks = hooksOf(`
export function HomePage() {
  return (
    <nav aria-label="Main navigation" data-testid="home-nav">
      <a href="/orders" data-cy="orders-link">Orders</a>
      <a href="/settings" data-testid={"settings-link"}>Settings</a>
    </nav>
  );
}
`);
    expect(hooks.get("data-testid:home-nav")).toMatchObject({ template_path: "src/pages/HomePage.tsx", conditional: false, repeated: false });
    expect(hooks.get("data-cy:orders-link")).toBeDefined();
    expect(hooks.get("data-testid:settings-link")).toBeDefined();
    expect(hooks.get("aria-label:Main navigation")).toBeDefined();
  });

  it("skips dynamic values the inventory cannot list", () => {
    const hooks = hooksOf(`
export function Row({ id }: { id: string }) {
  return <li data-testid={\`row-\${id}\`} data-cy={id}>x</li>;
}
`);
    expect(hooks.size).toBe(0);
  });

  // A selector alone says what exists, not when. The Angular enricher derives @if / @for from the
  // template; here the AST says it: a `&&` operand or a ternary branch is conditional, a `.map`
  // callback is repeated.
  it("marks conditional and repeated elements from the JSX structure", () => {
    const hooks = hooksOf(`
export function OrdersPage({ loading, orders }: { loading: boolean; orders: { id: string }[] }) {
  if (!orders) {
    return <p data-testid="orders-missing">none</p>;
  }
  return (
    <section data-testid="orders-root">
      {loading && <p data-testid="orders-loading">Loading…</p>}
      {orders.length === 0 ? <p data-testid="orders-empty">Empty</p> : null}
      <ul data-testid="orders-list">
        {orders.map((o) => (
          <li key={o.id} data-testid="orders-item">{o.id}</li>
        ))}
      </ul>
    </section>
  );
}
`);
    expect(hooks.get("data-testid:orders-root")).toMatchObject({ conditional: false, repeated: false });
    expect(hooks.get("data-testid:orders-loading")).toMatchObject({ conditional: true, repeated: false });
    expect(hooks.get("data-testid:orders-empty")).toMatchObject({ conditional: true, repeated: false });
    expect(hooks.get("data-testid:orders-item")).toMatchObject({ conditional: false, repeated: true });
    expect(hooks.get("data-testid:orders-missing")).toMatchObject({ conditional: true });
    expect(hooks.get("data-testid:orders-list")).toMatchObject({ conditional: false, repeated: false });
  });

  it("ignores attributes that are not test hooks", () => {
    const hooks = hooksOf(`export const X = () => <button type="button" className="btn" id="save">Save</button>;`);
    expect(hooks.size).toBe(0);
  });
});
