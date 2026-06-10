import { describe, expect, it } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  buildOpenAPIRouteLines,
  extractOpenAPIRoutesFromJSON,
  extractOpenAPIRoutesFromSpecContent,
  extractOpenAPIRoutesFromYAML,
} from "./openapi-routes";

describe("extractOpenAPIRoutesFromJSON", () => {
  it("resolves Path Item $ref (components.pathItems)", () => {
    const j = JSON.stringify({
      openapi: "3.0.0",
      paths: { "/pets": { $ref: "#/components/pathItems/PetsPath" } },
      components: {
        pathItems: {
          PetsPath: {
            get: { operationId: "listPets" },
            post: {},
          },
        },
      },
    });
    const r = extractOpenAPIRoutesFromJSON(j);
    expect(r.map((x) => `${x.method} ${x.path}`).sort()).toEqual(["GET /pets", "POST /pets"]);
    expect(r.find((x) => x.method === "GET")?.operationId).toBe("listPets");
  });

  it("resolves Operation $ref under a path", () => {
    const j = JSON.stringify({
      openapi: "3.0.0",
      paths: {
        "/items": {
          get: { $ref: "#/components/pathItems/Items/get" },
        },
      },
      components: {
        pathItems: {
          Items: {
            get: { operationId: "listItems" },
          },
        },
      },
    });
    const r = extractOpenAPIRoutesFromJSON(j);
    expect(r).toHaveLength(1);
    expect(r[0].operationId).toBe("listItems");
    expect(r[0].path).toBe("/items");
  });

  it("parses OpenAPI 3 paths", () => {
    const j = JSON.stringify({
      openapi: "3.0.0",
      paths: {
        "/pets": {
          get: { operationId: "listPets" },
          post: {},
        },
        "/pets/{id}": {
          get: {},
        },
      },
    });
    const r = extractOpenAPIRoutesFromJSON(j);
    expect(r.map((x) => `${x.method} ${x.path}`).sort()).toEqual([
      "GET /pets",
      "GET /pets/{id}",
      "POST /pets",
    ]);
    expect(r.find((x) => x.method === "GET" && x.path === "/pets")?.operationId).toBe("listPets");
  });

  it("returns empty on invalid JSON", () => {
    expect(extractOpenAPIRoutesFromJSON("not json")).toEqual([]);
  });
});

describe("extractOpenAPIRoutesFromYAML", () => {
  it("parses OpenAPI 3 paths from YAML", () => {
    const y = `
openapi: 3.0.0
paths:
  /v1/a:
    get:
      operationId: opA
  /v1/b:
    post: {}
`;
    const r = extractOpenAPIRoutesFromYAML(y);
    expect(r.map((x) => `${x.method} ${x.path}`).sort()).toEqual(["GET /v1/a", "POST /v1/b"]);
    expect(r.find((x) => x.path === "/v1/a")?.operationId).toBe("opA");
  });

  it("returns empty on invalid YAML", () => {
    expect(extractOpenAPIRoutesFromYAML(":\n  bad")).toEqual([]);
  });
});

describe("extractOpenAPIRoutesFromSpecContent", () => {
  it("uses YAML when document does not look like JSON", () => {
    const y = "swagger: '2.0'\npaths:\n  /legacy:\n    get: {}\n";
    const r = extractOpenAPIRoutesFromSpecContent(y);
    expect(r).toHaveLength(1);
    expect(r[0].method).toBe("GET");
    expect(r[0].path).toBe("/legacy");
  });
});

describe("buildOpenAPIRouteLines", () => {
  it("emits LangIndexerJSON when openapi.json exists at repo root", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "oa-"));
    fs.writeFileSync(
      path.join(root, "openapi.json"),
      JSON.stringify({
        openapi: "3.0.0",
        paths: { "/api/v1/hello": { get: {} } },
      }),
    );
    const lines = buildOpenAPIRouteLines(root);
    expect(lines.length).toBe(1);
    const api = lines[0].symbols.filter((s) => s.kind === "API_ROUTE");
    expect(api.length).toBe(1);
    expect(api[0].fq_name).toContain("GET:");
    expect(api[0].fq_name).toContain("/api/v1/hello");
    expect(api[0].fq_name).toContain("@openapi:openapi.json#");
  });

  it("emits from openapi.yaml when JSON absent", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "oa-yaml-"));
    fs.writeFileSync(
      path.join(root, "openapi.yaml"),
      "openapi: 3.0.0\npaths:\n  /yaml-only:\n    get:\n      operationId: y1\n",
    );
    const lines = buildOpenAPIRouteLines(root);
    const yamlLine = lines.find((l) => l.path === "openapi.yaml");
    expect(yamlLine).toBeDefined();
    const api = yamlLine!.symbols.filter((s) => s.kind === "API_ROUTE");
    expect(api.length).toBe(1);
    expect(api[0].fq_name).toContain("/yaml-only");
    expect(api[0].fq_name).toContain("@openapi:openapi.yaml#");
  });

  it("discovers spec/openapi.yaml under extended directories", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "oa-specdir-"));
    fs.mkdirSync(path.join(root, "spec"));
    fs.writeFileSync(
      path.join(root, "spec", "openapi.yaml"),
      "openapi: 3.0.0\npaths:\n  /ext:\n    get: {}\n",
    );
    const lines = buildOpenAPIRouteLines(root);
    const hit = lines.find((l) => l.path === "spec/openapi.yaml");
    expect(hit).toBeDefined();
    expect(hit!.symbols.some((s) => s.kind === "API_ROUTE")).toBe(true);
  });

  it("prefers openapi.json over openapi.yaml for the same stem", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "oa-pref-"));
    fs.writeFileSync(
      path.join(root, "openapi.json"),
      JSON.stringify({ openapi: "3.0.0", paths: { "/j": { get: {} } } }),
    );
    fs.writeFileSync(
      path.join(root, "openapi.yaml"),
      "openapi: 3.0.0\npaths:\n  /y:\n    get: {}\n",
    );
    const lines = buildOpenAPIRouteLines(root);
    const paths = lines.map((l) => l.path);
    expect(paths).toContain("openapi.json");
    expect(paths).not.toContain("openapi.yaml");
    const api = lines[0].symbols.filter((s) => s.kind === "API_ROUTE");
    expect(api.some((s) => s.fq_name.includes("/j"))).toBe(true);
  });
});
