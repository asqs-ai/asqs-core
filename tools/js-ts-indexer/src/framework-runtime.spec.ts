import { describe, expect, it } from "vitest";
import {
  parseFrameworksFlag,
  wantE2EEnricher,
  wantHttpClientEnricher,
  wantNest,
  wantNodeEnrichers,
  wantReact,
  wantTanStackRouter,
} from "./framework-runtime";
import type { ProjectDiscovery } from "./discovery";

const emptySignals = (): ProjectDiscovery["frameworkSignals"] => ({
  nest: false,
  react: false,
  angular: false,
  vue: false,
  solid: false,
  angularjs: false,
  tanstackRouter: false,
  nuxt: false,
});

function discoveryWith(s: Partial<ProjectDiscovery["frameworkSignals"]>): ProjectDiscovery {
  return {
    repoRoot: "/",
    packages: [],
    tsconfigs: [],
    angularProjects: [],
    frameworkSignals: { ...emptySignals(), ...s },
    testFramework: "",
    scripts: {},
    packageManager: "npm",
    nuxtPagePaths: [],
  };
}

describe("parseFrameworksFlag", () => {
  it("treats empty and auto as auto", () => {
    expect(parseFrameworksFlag("")).toBe("auto");
    expect(parseFrameworksFlag("auto")).toBe("auto");
  });
  it("parses comma and pipe lists", () => {
    const a = parseFrameworksFlag("nest, react");
    expect(a).toBeInstanceOf(Set);
    expect((a as Set<string>).has("nest")).toBe(true);
    expect((a as Set<string>).has("react")).toBe(true);
  });
});

describe("wantNodeEnrichers", () => {
  it("none disables node", () => {
    expect(wantNodeEnrichers("none")).toBe(false);
  });
  it("auto enables node", () => {
    expect(wantNodeEnrichers("auto")).toBe(true);
  });
  it("explicit requires token", () => {
    expect(wantNodeEnrichers(new Set(["react"]))).toBe(false);
    expect(wantNodeEnrichers(new Set(["node"]))).toBe(true);
  });
});

describe("wantReact", () => {
  it("none is false", () => {
    expect(wantReact("none", discoveryWith({ react: true }))).toBe(false);
  });
  it("auto follows discovery", () => {
    expect(wantReact("auto", discoveryWith({ react: false }))).toBe(false);
    expect(wantReact("auto", discoveryWith({ react: true }))).toBe(true);
  });
  it("explicit requires token and signal", () => {
    expect(wantReact(new Set(["react"]), discoveryWith({ react: false }))).toBe(false);
    expect(wantReact(new Set(["react"]), discoveryWith({ react: true }))).toBe(true);
  });
});

describe("wantNest", () => {
  it("explicit nest with signal", () => {
    expect(wantNest(new Set(["nest"]), discoveryWith({ nest: true }))).toBe(true);
    expect(wantNest(new Set(["nest"]), discoveryWith({ nest: false }))).toBe(false);
  });
});

describe("wantHttpClientEnricher", () => {
  it("auto on, none off, explicit needs http", () => {
    expect(wantHttpClientEnricher("auto")).toBe(true);
    expect(wantHttpClientEnricher("none")).toBe(false);
    expect(wantHttpClientEnricher(new Set(["react"]))).toBe(false);
    expect(wantHttpClientEnricher(new Set(["http"]))).toBe(true);
  });
});

describe("wantE2EEnricher", () => {
  it("auto on, none off, explicit needs e2e", () => {
    expect(wantE2EEnricher("auto")).toBe(true);
    expect(wantE2EEnricher("none")).toBe(false);
    expect(wantE2EEnricher(new Set(["nest"]))).toBe(false);
    expect(wantE2EEnricher(new Set(["e2e"]))).toBe(true);
  });
});

describe("wantTanStackRouter", () => {
  it("follows tanstackRouter signal and explicit flag", () => {
    expect(wantTanStackRouter("none", discoveryWith({ tanstackRouter: true }))).toBe(false);
    expect(wantTanStackRouter("auto", discoveryWith({ tanstackRouter: false }))).toBe(false);
    expect(wantTanStackRouter("auto", discoveryWith({ tanstackRouter: true }))).toBe(true);
    expect(wantTanStackRouter(new Set(["tanstack"]), discoveryWith({ tanstackRouter: false }))).toBe(true);
  });
});
