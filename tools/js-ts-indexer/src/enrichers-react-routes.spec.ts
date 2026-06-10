import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import {
  enrichCreateBrowserRouter,
  enrichFileReactRouter,
  joinReactRouterPaths,
  pageRouteFQName,
} from "./enrichers-react-routes";
import { enrichCreateRoutesFromElements } from "./enrichers-page-route-common";
import type { FileSymbolsEdges } from "./enrichers";

describe("joinReactRouterPaths", () => {
  it("resolves relative child under parent", () => {
    expect(joinReactRouterPaths("/app", "settings")).toBe("/app/settings");
  });
  it("keeps absolute child", () => {
    expect(joinReactRouterPaths("/app", "/other")).toBe("/other");
  });
});

describe("pageRouteFQName", () => {
  it("normalizes path and encodes module + line", () => {
    expect(pageRouteFQName("/dashboard", "src.routes", 12)).toBe(
      "PAGE_ROUTE:/dashboard@src.routes:L12",
    );
  });
});

describe("enrichFileReactRouter", () => {
  it("emits PAGE_ROUTE for Route path and CONTAINS from module", () => {
    const source = `
import { Routes, Route } from 'react-router-dom';

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/admin" element={<div />} />
    </Routes>
  );
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReactRouter(sf, entry, "src.routes");

    const routes = entry.symbols.filter((s) => s.kind === "PAGE_ROUTE");
    expect(routes.length).toBe(1);
    expect(routes[0].fq_name).toContain("PAGE_ROUTE:/admin@");
    expect(routes[0].signature).toMatchObject({
      framework: "react_router_jsx",
      path_pattern: "/admin",
    });
    expect(entry.edges.some((e) => e.edge_type === "CONTAINS" && e.callee_fq_name === routes[0].fq_name)).toBe(
      true,
    );
  });

  it("emits PAGE_ROUTE for PrivateRoute-style wrappers with path", () => {
    const source = `
import { Routes, PrivateRoute } from 'react-router-dom';

export function AppRoutes() {
  return (
    <Routes>
      <PrivateRoute path="/admin" element={<div />} />
    </Routes>
  );
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReactRouter(sf, entry, "src.routes");
    const routes = entry.symbols.filter((s) => s.kind === "PAGE_ROUTE");
    expect(routes.length).toBe(1);
    expect(routes[0].fq_name).toContain("PAGE_ROUTE:/admin@");
  });

  it("emits PAGE_ROUTE for path={\`/x\`} (no-substitution template)", () => {
    const source = `
import { Routes, Route } from 'react-router-dom';
export function X() {
  return (
    <Routes>
      <Route path={\`/about\`} element={null} />
    </Routes>
  );
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReactRouter(sf, entry, "src.routes");
    const routes = entry.symbols.filter((s) => s.kind === "PAGE_ROUTE");
    expect(routes.length).toBe(1);
    expect((routes[0].signature as { path_pattern?: string })?.path_pattern).toBe("/about");
  });

  it("emits PAGE_ROUTE from createBrowserRouter config", () => {
    const source = `
import { createBrowserRouter } from 'react-router-dom';

export const router = createBrowserRouter([
  { path: '/dashboard', element: null },
  {
    path: '/app',
    children: [{ path: 'settings', element: null }],
  },
]);
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("router.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichCreateBrowserRouter(sf, entry, "src.router");

    const paths = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern);
    expect(paths).toContain("/dashboard");
    expect(paths).toContain("/app");
    expect(paths).toContain("/app/settings");
  });

  it("resolves createBrowserRouter(ROUTES) when ROUTES is imported from another file", () => {
    const project = new Project({ useInMemoryFileSystem: true });
    project.createSourceFile(
      "routes.tsx",
      `
import type { RouteObject } from 'react-router-dom';
export const ROUTES: RouteObject[] = [
  { path: '/shop', element: null },
  { path: '/other', element: null },
];
`.trim(),
    );
    const mainSf = project.createSourceFile(
      "main.tsx",
      `
import { createBrowserRouter } from 'react-router-dom';
import { ROUTES } from './routes';
export const router = createBrowserRouter(ROUTES);
`.trim(),
    );
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichCreateBrowserRouter(mainSf, entry, "src.main");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern)
      .sort();
    expect(patterns).toContain("/shop");
    expect(patterns).toContain("/other");
  });

  it("resolves createBrowserRouter(ROUTES) when ROUTES is a same-file const", () => {
    const source = `
import { createBrowserRouter, Navigate, RouteObject } from 'react-router-dom';

export const ROUTES: RouteObject[] = [
  {
    path: '/',
    children: [
      { index: true, element: <Navigate replace to="/main" /> },
      { path: '/auth', element: null },
      { path: '/main', element: null },
    ],
  },
];

export const router = createBrowserRouter(ROUTES, { basename: '/app' });
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichCreateBrowserRouter(sf, entry, "src.routes");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern)
      .sort();
    expect(patterns).toContain("/");
    expect(patterns).toContain("/auth");
    expect(patterns).toContain("/main");
  });

  it("emits PAGE_ROUTE from createHashRouter config", () => {
    const source = `
import { createHashRouter } from 'react-router-dom';
export const router = createHashRouter([{ path: '/legacy', element: null }]);
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("router.ts", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichCreateBrowserRouter(sf, entry, "src.router");
    const paths = entry.symbols.filter((s) => s.kind === "PAGE_ROUTE");
    expect(paths.length).toBe(1);
    expect((paths[0].signature as { path_pattern?: string })?.path_pattern).toBe("/legacy");
  });

  it("joins nested JSX Route paths under a parent Route", () => {
    const source = `
import { Routes, Route } from 'react-router-dom';

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/app" element={<div />}>
        <Route path="settings" element={<div />} />
      </Route>
    </Routes>
  );
}
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("routes.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileReactRouter(sf, entry, "src.routes");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern)
      .sort();
    expect(patterns).toContain("/app");
    expect(patterns).toContain("/app/settings");
  });

  it("emits PAGE_ROUTE from createRoutesFromElements with nested paths", () => {
    const source = `
import { createRoutesFromElements, Route } from 'react-router-dom';

export const r = createRoutesFromElements(
  <Route path="/" element={null}>
    <Route path="team" element={null} />
  </Route>
);
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("elts.tsx", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichCreateRoutesFromElements(sf, entry, "src.elts", "react_router_elements");

    const patterns = entry.symbols
      .filter((s) => s.kind === "PAGE_ROUTE")
      .map((s) => (s.signature as { path_pattern?: string })?.path_pattern)
      .sort();
    expect(patterns).toContain("/");
    expect(patterns).toContain("/team");
  });
});
