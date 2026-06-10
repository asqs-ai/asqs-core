import { describe, expect, it } from "vitest";
import { buildNuxtFileRouteLines, nuxtPagePathToRoutePattern } from "./nuxt-file-routes";
import type { ProjectDiscovery } from "./discovery";

describe("nuxtPagePathToRoutePattern", () => {
  it("maps index and nested static segments", () => {
    expect(nuxtPagePathToRoutePattern("pages/index.vue")).toBe("/");
    expect(nuxtPagePathToRoutePattern("pages/about.vue")).toBe("/about");
    expect(nuxtPagePathToRoutePattern("pages/users/index.vue")).toBe("/users");
    expect(nuxtPagePathToRoutePattern("pages/users/profile.vue")).toBe("/users/profile");
  });

  it("maps dynamic and optional segments", () => {
    expect(nuxtPagePathToRoutePattern("pages/users/[id].vue")).toBe("/users/:id");
    expect(nuxtPagePathToRoutePattern("pages/[[slug]].vue")).toBe("/:slug?");
    expect(nuxtPagePathToRoutePattern("pages/docs/[...path].vue")).toBe("/docs/:path(.*)");
  });

  it("returns undefined for non-nuxt paths", () => {
    expect(nuxtPagePathToRoutePattern("src/App.vue")).toBeUndefined();
    expect(nuxtPagePathToRoutePattern("pages/foo.ts")).toBeUndefined();
  });
});

describe("buildNuxtFileRouteLines", () => {
  it("emits PAGE_ROUTE per nuxt page when frameworkSignals.nuxt", () => {
    const discovery: ProjectDiscovery = {
      repoRoot: "/r",
      packages: [],
      tsconfigs: [],
      angularProjects: [],
      frameworkSignals: {
        nest: false,
        react: false,
        angular: false,
        vue: true,
        solid: false,
        angularjs: false,
        tanstackRouter: false,
        nuxt: true,
      },
      nuxtPagePaths: ["pages/index.vue", "pages/blog/[slug].vue"],
      testFramework: "",
      scripts: {},
      packageManager: "npm",
    };
    const lines = buildNuxtFileRouteLines(discovery);
    expect(lines.length).toBe(2);
    const kinds = lines.flatMap((l) => l.symbols.map((s) => s.kind));
    expect(kinds.filter((k) => k === "PAGE_ROUTE")).toHaveLength(2);
    const fq = lines[1].symbols.find((s) => s.kind === "PAGE_ROUTE")?.fq_name ?? "";
    expect(fq).toContain("PAGE_ROUTE:");
    expect(fq).toContain("/blog/:slug");
  });

  it("returns empty when nuxt is false", () => {
    const discovery: ProjectDiscovery = {
      repoRoot: "/r",
      packages: [],
      tsconfigs: [],
      angularProjects: [],
      frameworkSignals: {
        nest: false,
        react: false,
        angular: false,
        vue: true,
        solid: false,
        angularjs: false,
        tanstackRouter: false,
        nuxt: false,
      },
      nuxtPagePaths: ["pages/index.vue"],
      testFramework: "",
      scripts: {},
      packageManager: "npm",
    };
    expect(buildNuxtFileRouteLines(discovery)).toEqual([]);
  });
});
