import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichFileAngularJsGraph } from "./enrichers-angularjs-graph";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichFileAngularJsGraph", () => {
  it("parses fluent angular.module().controller() chain", () => {
    const source = `
angular.module('myApp', []).controller('MainCtrl', function($scope) {});
`;
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("app.js", source.trim());
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichFileAngularJsGraph(sf, entry, "app");
    expect(entry.symbols.some((s) => s.kind === "ANGULARJS_MODULE")).toBe(true);
    expect(entry.symbols.some((s) => s.kind === "ANGULARJS_CONTROLLER")).toBe(true);
  });
});
